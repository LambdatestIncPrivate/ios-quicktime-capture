#include <stdint.h>
#include "decoder_darwin.h"
#include <unistd.h>
#include <stdbool.h>
#include <stdio.h>
#include <string.h>
#include <arpa/inet.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavutil/imgutils.h>
#include <libavutil/error.h>
#include <stdatomic.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/avfilter.h>

typedef struct FilteringContext
{
    AVFilterContext *buffersrc_ctx;
    AVFilterContext *buffersink_ctx;
    AVFilterGraph *filter_graph;
} FilteringContext;

#define BUFFER_SIZE 12
static const AVRational TIME_BASE = {1, 1000000000}; // timestamps in us
#define PACKET_HEADER_SIZE 12
#define PACKET_ORIENTATION_WIDTH_HEIGHT 12
#define PACKET_FLAG_KEY_FRAME (UINT64_C(1) << 62)
#define PACKET_FLAG_CONFIG (UINT64_C(1) << 63)
#define PACKET_PTS_MASK (PACKET_FLAG_KEY_FRAME - 1)
extern void GoLoggingCallback(char *msg);
// Global cancellation flag
_Atomic bool cancelled = false;

static uint64_t initial_pts = UINT64_MAX; // Use UINT64_MAX to indicate unset initial PTS

// Function to set the cancellation flag
void set_cancelled(bool state)
{
    atomic_store(&cancelled, state);
}

// Function to set the timeout for socket operations
int set_socket_timeout(int sock, long sec, long usec) {
    struct timeval timeout;
    timeout.tv_sec = sec;
    timeout.tv_usec = usec;

    // Set receive timeout
    if (setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, (const char*)&timeout, sizeof(timeout)) < 0) {
        perror("setsockopt failed");
        return -1;
    }
    return 0;
}

int init_filter_graph(FilteringContext *fctx, int width, int height, enum AVPixelFormat pix_fmt)
{
    char args[512];
    int ret = 0;
    const AVFilter *buffersrc = avfilter_get_by_name("buffer");
    const AVFilter *buffersink = avfilter_get_by_name("buffersink");
    AVFilterContext *buffersrc_ctx = NULL;
    AVFilterContext *buffersink_ctx = NULL;
    AVFilterGraph *filter_graph = avfilter_graph_alloc();

    if (!filter_graph)
    {
        GoLoggingCallback("Could not allocate filter graph.");
        return AVERROR(ENOMEM);
    }

    // Prepare arguments for the buffer source filter
    snprintf(args, sizeof(args),
             "video_size=%dx%d:pix_fmt=%d:time_base=1/1000000000:pixel_aspect=1/1",
             height, width, pix_fmt);

    // Create the buffer source filter context
    ret = avfilter_graph_create_filter(&buffersrc_ctx, buffersrc, "in", args, NULL, filter_graph);
    if (ret < 0)
    {
        GoLoggingCallback("Could not create buffer source.");
        goto end;
    }

    // Create the buffer sink filter context
    ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, filter_graph);
    if (ret < 0)
    {
        GoLoggingCallback("Could not create buffer sink.");
        goto end;
    }

    // Create and add the rotate filter; rotates by 90 degrees anticlockwise
    AVFilterContext *rotate_ctx;

    ret = avfilter_graph_create_filter(&rotate_ctx, avfilter_get_by_name("transpose"), "rotate", "dir=clock", NULL, filter_graph);
    if (ret < 0)
    {
        GoLoggingCallback("Could not create rotate filter.");
        goto end;
    }

    // Link the filters together: buffersrc -> rotate -> buffersink
    ret = avfilter_link(buffersrc_ctx, 0, rotate_ctx, 0);
    if (ret >= 0)
        ret = avfilter_link(rotate_ctx, 0, buffersink_ctx, 0);
    if (ret < 0)
    {
        GoLoggingCallback("Error connecting filters.");
        goto end;
    }

    // Configure the graph
    ret = avfilter_graph_config(filter_graph, NULL);
    if (ret < 0)
    {
        GoLoggingCallback("Error configuring the filter graph.");
        goto end;
    }

    // Fill FilteringContext struct
    fctx->buffersrc_ctx = buffersrc_ctx;
    fctx->buffersink_ctx = buffersink_ctx;
    fctx->filter_graph = filter_graph;
    return 0;

end:
    if (filter_graph)
        avfilter_graph_free(&filter_graph);
    return ret;
}

int filter_frame(FilteringContext *fctx, AVFrame *frame, AVFrame *filt_frame)
{
    int ret;
    // Send the frame to the input of the filtergraph
    ret = av_buffersrc_add_frame_flags(fctx->buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF);
    if (ret < 0)
    {
        GoLoggingCallback("Error while feeding the filtergraph");
        return ret;
    }
    // Get the filtered frame from the output of the filtergraph
    ret = av_buffersink_get_frame(fctx->buffersink_ctx, filt_frame);
    if (ret < 0)
    {
        // If no more frames are available for filtering, it's not necessarily an error
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF)
            return 0;
        GoLoggingCallback("Error while getting the filtered frame");
        return ret;
    }
    return 0;
}

// Function to log error messages
static void log_error(int err)
{
    char err_buf[AV_ERROR_MAX_STRING_SIZE] = {0};
    av_strerror(err, err_buf, AV_ERROR_MAX_STRING_SIZE);
    fprintf(stderr, "Error: %s\n", err_buf);
}

// Function to log custom messages
static void custom_log(const char *msg)
{
    GoLoggingCallback(msg);
    // printf("Encoder --> %s\n", msg);
}
static void LOG_OOM(void)
{
    custom_log("Out of memory");
    fprintf(stderr, "Out of memory");
}

ssize_t net_recv_all(int soc, void *buf, size_t len)
{
    return recv(soc, buf, len, MSG_WAITALL);
}

static inline void recorder_rescale_packet(AVStream *stream, AVPacket *packet)
{
    av_packet_rescale_ts(packet, TIME_BASE, stream->time_base);
}
static AVPacket *recorder_packet_ref(const AVPacket *packet)
{
    AVPacket *p = av_packet_alloc();
    if (!p)
    {
        custom_log("OOM - av_packet_alloc failed");
        return NULL;
    }

    if (av_packet_ref(p, packet))
    {
        av_packet_free(&p);
        return NULL;
    }

    return p;
}

static inline uint32_t read32be(const uint8_t *buf)
{
    return ((uint32_t)buf[0] << 24) | (buf[1] << 16) | (buf[2] << 8) | buf[3];
}

static inline uint64_t read64be(const uint8_t *buf)
{
    return ((uint64_t)buf[0] << 56) |
           ((uint64_t)buf[1] << 48) |
           ((uint64_t)buf[2] << 40) |
           ((uint64_t)buf[3] << 32) |
           ((uint64_t)buf[4] << 24) |
           ((uint64_t)buf[5] << 16) |
           ((uint64_t)buf[6] << 8) |
           (uint64_t)buf[7];
}

static const AVOutputFormat *find_muxer(const char *name)
{
    const AVOutputFormat *fmt = NULL;
    void *opaque = NULL;
    while ((fmt = av_muxer_iterate(&opaque)))
    {
        if (fmt->name && !strcmp(fmt->name, name))
        {
            return fmt;
        }
    }
    return NULL;
}

static bool demuxer_recv_packet(int *sock, AVPacket *packet)
{
    // The video and audio streams contain a sequence of raw packets (as
    // provided by MediaCodec), each prefixed with a "meta" header.
    //
    // The "meta" header length is 12 bytes:
    // [. . . . . . . .|. . . .]. . . . . . . . . . . . . . . ...
    //  <-------------> <-----> <-----------------------------...
    //        PTS        packet        raw packet
    //                    size
    //
    // It is followed by <packet_size> bytes containing the packet/frame.
    //
    // The most significant bits of the PTS are used for packet flags:
    //
    //  byte 7   byte 6   byte 5   byte 4   byte 3   byte 2   byte 1   byte 0
    // CK...... ........ ........ ........ ........ ........ ........ ........
    // ^^<------------------------------------------------------------------->
    // ||                                PTS
    // | `- key frame
    //  `-- config packet

    uint8_t header[PACKET_HEADER_SIZE];
    ssize_t r = net_recv_all(*sock, header, PACKET_HEADER_SIZE);
    if (r < PACKET_HEADER_SIZE)
    {
        return false;
    }
    uint64_t pts = read64be(header);
    uint32_t len = read32be(&header[8]);
    if (av_new_packet(packet, len))
    {
        LOG_OOM();
        return false;
    }
    r = net_recv_all(*sock, packet->data, len);
    if (r < 0 || ((uint32_t)r) < len)
    {
        av_packet_unref(packet);
        return false;
    }

    if (initial_pts == UINT64_MAX && pts != 0 && !(pts & PACKET_FLAG_CONFIG)) {
        initial_pts = pts & PACKET_PTS_MASK; // Set initial PTS on the first non-config packet
    }
    if (pts > 0)
    {
        packet->pts = (pts & PACKET_PTS_MASK) - initial_pts;
        packet->dts = packet->pts;
    }
    if (pts & PACKET_FLAG_CONFIG)
    {
        custom_log("packet is config frame");
        packet->pts = AV_NOPTS_VALUE;
    }
    if (pts & PACKET_FLAG_KEY_FRAME)
    {
        custom_log("packet is key frame");
        packet->flags |= AV_PKT_FLAG_KEY;
    }
    return true;
}

// Function to initialize and run the TCP server
int convert_to_mp4(const char *output_filename, const uint32_t port_number, const char *address)
{
    AVFormatContext *input_format_context = NULL, *output_format_context = NULL;
    AVCodecContext *codec_context = NULL;
    AVCodec *codec = NULL;
    AVStream *out_stream = NULL;
    // AVPacket packet;
    int ret;

    // open a simple tcp socket client at localhost:9009 and read the data
    int sock;
    struct sockaddr_in serverAddr;
    char buffer[BUFFER_SIZE];

    // Create socket
    sock = socket(AF_INET, SOCK_STREAM, 0);

    // Set server address
    memset(&serverAddr, 0, sizeof(serverAddr));
    serverAddr.sin_family = AF_INET;
    serverAddr.sin_port = htons(port_number);
    serverAddr.sin_addr.s_addr = inet_addr(address);

    // Connect to the server
    custom_log("connecting to server");
    if (connect(sock, (struct sockaddr *)&serverAddr, sizeof(serverAddr)) < 0)
    {
        custom_log("Connection failed");
        close(sock); // Fix: Close socket on error
        return -1;   // Early return on error
    }

    // -----
    // Find the decoder for the h264
    codec = avcodec_find_decoder(AV_CODEC_ID_H264);
    if (!codec)
    {
        custom_log("Codec not found");
        return -1; // Codec not found
    }

    codec_context = avcodec_alloc_context3(codec);
    if (!codec_context)
    {
        LOG_OOM();
        custom_log("Could not allocate video codec context");
        close(sock);
        return -1; 
    }
    AVPacket *packet = av_packet_alloc();
    if (!packet)
    {
        LOG_OOM();
        avcodec_free_context(&codec_context); // Fix: Free codec_context on error
        close(sock);                          
        return -1;                           
    }
    bool ok = demuxer_recv_packet(&sock, packet);
    if (!ok)
    {
        custom_log("end of stream");
        goto exit;
    }
    if (packet->pts != AV_NOPTS_VALUE)
    {
        custom_log("packet pts: isn't config packet, first packet must be config always");
        goto exit;
    }
    uint8_t header[PACKET_ORIENTATION_WIDTH_HEIGHT];
    ssize_t r = net_recv_all(sock, header, PACKET_ORIENTATION_WIDTH_HEIGHT);
    if (r < PACKET_ORIENTATION_WIDTH_HEIGHT)
    {
        custom_log("end of stream");
        goto exit;
    }

    uint64_t width = read32be(header);
    uint32_t height = read32be(&header[4]);
    uint32_t orientation = read32be(&header[8]);

    codec_context->width = width;
    codec_context->height = height;
    codec_context->flags |= AV_CODEC_FLAG_LOW_DELAY;
    codec_context->pix_fmt = AV_PIX_FMT_YUV420P;

    // Open codec
    if ((ret = avcodec_open2(codec_context, codec, NULL)) < 0)
    {
        log_error(ret);
        return ret; // Could not open codec
    }

    // format setting
    const char *format_name = "mp4";
    const AVOutputFormat *format = find_muxer(format_name);
    if (!format)
    {
        custom_log("Could not find muxer");
        return false;
    }

    output_format_context = avformat_alloc_context();
    if (!output_format_context)
    {
        custom_log("Could not allocate output context");
        fprintf(stderr, "Could not allocate output context\n");
        return -1;
    }

    avio_open(&output_format_context->pb, output_filename, AVIO_FLAG_WRITE);
    output_format_context->oformat = format;
    av_dict_set(&output_format_context->metadata, "title", "Recorder", 0);
    custom_log("output format context created");
    custom_log("recorded file");
    out_stream = avformat_new_stream(output_format_context, codec_context->codec);
    if (!out_stream)
    {
        fprintf(stderr, "Failed allocating output stream\n");
        custom_log("Failed allocating output stream");
        return -1;
    }
    custom_log("output stream created");
    out_stream->start_time = 0;
    ret = avcodec_parameters_from_context(out_stream->codecpar, codec_context);
    if (ret < 0)
    {
        fprintf(stderr, "Failed to copy codec parameters to output stream\n");
        custom_log("Failed to copy codec parameters to output stream");
       return -1;
    }
    printf("codec parameters copied to output stream");
    FilteringContext fctx;
    AVCodec *encoder;
    AVCodecContext *encoder_ctx;

    if (orientation == 1)
    {
        out_stream->codecpar->width = height;
        out_stream->codecpar->height = width;
        encoder = avcodec_find_encoder(AV_CODEC_ID_H264);
        if (!encoder)
        {
            GoLoggingCallback("Codec not found");
            return -1;
        }

        encoder_ctx = avcodec_alloc_context3(encoder);
        if (!encoder_ctx)
        {
            GoLoggingCallback("Could not allocate video codec context");
            return -1;
        }

        encoder_ctx->height = width;
        encoder_ctx->width = height;
        encoder_ctx->pix_fmt = AV_PIX_FMT_YUV420P;
        encoder_ctx->time_base = TIME_BASE;

        if (avcodec_open2(encoder_ctx, encoder, NULL) < 0)
        {
            GoLoggingCallback("Could not open codec");
            return -1;
        }

        if (init_filter_graph(&fctx, codec_context->height, codec_context->width, codec_context->pix_fmt) < 0)
        {
            GoLoggingCallback("Could not initialize the filter graph");
            return -1;
        }
    }

    ret = avformat_write_header(output_format_context, NULL);
    if (ret < 0)
    {
        // Log the error
        char err_buf[AV_ERROR_MAX_STRING_SIZE];
        av_strerror(ret, err_buf, AV_ERROR_MAX_STRING_SIZE);
        fprintf(stderr, "Failed to write header: %s\n", err_buf);
        // Handle the error appropriately
        return -1;
    }
    custom_log("done writing header");
    set_socket_timeout(sock, 20, 0);
    for (;;)
    {
        if (atomic_load(&cancelled))
        {
            custom_log("cancellation requested");
            break;
        }
        bool ok = demuxer_recv_packet(&sock, packet);
        if (!ok)
        {
            custom_log("end of stream");
            break;
        }

        AVPacket *rec = recorder_packet_ref(packet);
        if (!rec)
        {
            LOG_OOM();
            return 0;
        }

        AVStream *video_stream = output_format_context->streams[0];
        recorder_rescale_packet(video_stream, rec);
        if (orientation == 1)
        {
            if (avcodec_send_packet(codec_context, rec) < 0)
            {
                // Handle send packet error
                av_packet_unref(rec);
                GoLoggingCallback("Error while sending packet to decoder");
                continue;
            }
            AVFrame *frame = av_frame_alloc();
            int response = 0;
            response = avcodec_receive_frame(codec_context, frame);
            if (response < 0)
            {
                av_frame_free(&frame);
                av_packet_unref(rec);
                continue; // Need more data
            }
            AVFrame *filt_frame = av_frame_alloc();
            if (!filt_frame)
            {
                GoLoggingCallback("OOM - av_frame_alloc failed");
                av_frame_free(&frame);
                av_packet_unref(rec);
                continue;
            }

            if (filter_frame(&fctx, frame, filt_frame) < 0)
            {
                GoLoggingCallback("Error while filtering frame");
                av_frame_free(&frame);
                av_packet_unref(rec);
            }
            // Encode frame
            if (avcodec_send_frame(encoder_ctx, filt_frame) < 0)
            {
                av_frame_free(&frame);
                av_packet_unref(rec);
                av_frame_free(&filt_frame);
                GoLoggingCallback("Error while sending frame to encoder, skipping");
                continue;
            }
            AVPacket *out_packet = av_packet_alloc();
            if (avcodec_receive_packet(encoder_ctx, out_packet) < 0)
            {
                av_frame_free(&frame);
                av_packet_unref(rec);
                av_frame_free(&filt_frame);
                GoLoggingCallback("Error while receiving packet from encoder, skipping");
                continue;
            }
            av_interleaved_write_frame(output_format_context, out_packet);
            av_packet_free(&out_packet);
            av_packet_unref(rec);
            av_frame_free(&frame);
            av_frame_free(&filt_frame);
        }
        else
        {
            av_interleaved_write_frame(output_format_context, rec);
            av_packet_unref(rec);
        }
    }
exit:
    if (output_format_context)
    {
        custom_log("closing output format context");
        int ret = 0;
        ret = av_write_trailer(output_format_context);
        if (ret < 0) {
            char error_buf[AV_ERROR_MAX_STRING_SIZE];
            av_strerror(ret, error_buf, AV_ERROR_MAX_STRING_SIZE);
            custom_log("write trailer error");
            custom_log(error_buf);
        }
        ret = avio_closep(&output_format_context->pb);
        if (ret < 0) {
            char error_buf[AV_ERROR_MAX_STRING_SIZE];
            av_strerror(ret, error_buf, AV_ERROR_MAX_STRING_SIZE);
            custom_log("avio closep error");
            custom_log(error_buf);
        }
        avformat_free_context(output_format_context);
        custom_log("output format context closed");
    }
    if (codec_context)
    {
        avcodec_free_context(&codec_context); // Fix: Free codec_context when done
    }
    if (packet)
    {
        av_packet_free(&packet); // Fix: Free packet when done
    }
    if (sock != -1)
    {
        close(sock); // Fix: Ensure the socket is closed
    }

    return 0; // Success
}