package decoder

/*
#cgo pkg-config: libavformat libavcodec libavutil
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

#define BUFFER_SIZE 12
static const AVRational SCRCPY_TIME_BASE = {1, 1000000000}; // timestamps in us
#define PACKET_HEADER_SIZE 12
#define PACKET_WIDTH_HEIGHT 8
#define PACKET_FLAG_KEY_FRAME (UINT64_C(1) << 62)
#define PACKET_FLAG_CONFIG (UINT64_C(1) << 63)
#define PACKET_PTS_MASK (PACKET_FLAG_KEY_FRAME - 1)

// Global cancellation flag
_Atomic bool cancelled = false;

// Function to set the cancellation flag
void set_cancelled(bool state) {
    atomic_store(&cancelled, state);
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
    printf("Encoder --> %s\n", msg);
}
static void LOG_OOM(void)
{
    fprintf(stderr, "Out of memory\n");
}

ssize_t net_recv_all(int soc, void *buf, size_t len)
{
    return recv(soc, buf, len, MSG_WAITALL);
}

static inline void recorder_rescale_packet(AVStream *stream, AVPacket *packet)
{
    av_packet_rescale_ts(packet, SCRCPY_TIME_BASE, stream->time_base);
}
static AVPacket *recorder_packet_ref(const AVPacket *packet)
{
    AVPacket *p = av_packet_alloc();
    if (!p)
    {
        LOG_OOM();
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
    ssize_t r = net_recv_all(sock, header, PACKET_HEADER_SIZE);
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
    r = net_recv_all(sock, packet->data, len);
    if (r < 0 || ((uint32_t)r) < len)
    {
        av_packet_unref(packet);
        return false;
    }
    if (pts > 0)
    {
        packet->pts = pts & PACKET_PTS_MASK;
        packet->dts = packet->pts;
    }
    if (pts & PACKET_FLAG_CONFIG)
    {
        custom_log("packet is config frame\n");
        packet->pts = AV_NOPTS_VALUE;
    }
    if (pts & PACKET_FLAG_KEY_FRAME)
    {
        custom_log("packet is key frame\n");
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
    custom_log("connecting to server\n");
    if (connect(sock, (struct sockaddr *)&serverAddr, sizeof(serverAddr)) < 0)
    {
        custom_log("Connection failed\n");
        close(sock); // Fix: Close socket on error
        return -1;   // Early return on error
    }

    // -----
    // Find the decoder for the h264
    codec = avcodec_find_decoder(AV_CODEC_ID_H264);
    if (!codec)
    {
        custom_log("Codec not found\n");
        exit(1);
    }

    codec_context = avcodec_alloc_context3(codec);
    if (!codec_context)
    {
        LOG_OOM();
        custom_log("Could not allocate video codec context\n");
        close(sock);
        exit(1);
    }
    AVPacket *packet = av_packet_alloc();
    if (!packet)
    {
        LOG_OOM();
        avcodec_free_context(&codec_context); // Fix: Free codec_context on error
        close(sock);                          // Fix: Close socket on error
        return -1;                            // Early return on error
    }
    bool ok = demuxer_recv_packet(sock, packet);
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
    uint8_t header[PACKET_WIDTH_HEIGHT];
    ssize_t r = net_recv_all(sock, header, PACKET_WIDTH_HEIGHT);
    if (r < PACKET_WIDTH_HEIGHT)
    {
        custom_log("end of stream");
        goto exit;
    }

    uint64_t width = read32be(header);
    uint32_t height = read32be(&header[4]);

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
        exit(1);
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
        exit(1);
    }
    custom_log("output stream created");
    ret = avcodec_parameters_from_context(out_stream->codecpar, codec_context);
    if (ret < 0)
    {
        fprintf(stderr, "Failed to copy codec parameters to output stream\n");
        exit(1);
    }
    printf("codec parameters copied to output stream\n");
    ret = avformat_write_header(output_format_context, NULL);
    if (ret < 0) {
        // Log the error
        char err_buf[AV_ERROR_MAX_STRING_SIZE];
        av_strerror(ret, err_buf, AV_ERROR_MAX_STRING_SIZE);
        fprintf(stderr, "Failed to write header: %s\n", err_buf);
        // Handle the error appropriately
        return -1;
    }
    printf("done writing header\n");
    custom_log("done writing header");
    for (;;)
    {
		if (atomic_load(&cancelled)) {
            custom_log("cancellation requested");
			break;
		}
        bool ok = demuxer_recv_packet(sock, packet);
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
        av_interleaved_write_frame(output_format_context, rec);
        av_packet_unref(rec);
    }
exit:
    if (output_format_context)
    {
        custom_log("closing output format context");
        av_write_trailer(output_format_context);
        avio_closep(&output_format_context->pb);
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
*/
import "C"
import (
	"context"
	"fmt"
	"unsafe"
)

type Decoder struct {
	ctx        context.Context
	address    string
	portNumber int
}

func NewDecoder(ctx context.Context, address string, portNumber int) *Decoder {
	if address == "" {
		address = "127.0.0.1"
	}
	return &Decoder{ctx: ctx, address: address, portNumber: portNumber}
}

func (d *Decoder) Decode(outputFilename string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic occurred in C.convert_to_mp4: %v", r)
		}
	}()
	outputFilenameC := C.CString(outputFilename)
	defer C.free(unsafe.Pointer(outputFilenameC))
	go func() {
		<-d.ctx.Done()
		d.cancel()
	}()
	ret := C.convert_to_mp4(outputFilenameC, C.uint32_t(d.portNumber), C.CString(d.address))
	if ret != 0 {
		return fmt.Errorf("error converting to mp4, exit status not zero")
	}
	return nil
}

func (d *Decoder) cancel() {
	C.set_cancelled(true)
}
