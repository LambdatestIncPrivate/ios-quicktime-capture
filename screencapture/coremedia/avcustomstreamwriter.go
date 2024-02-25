package coremedia

import (
	"encoding/binary"
	"io"
)

type AVCustomWriter struct {
	writer io.Writer
}

func NewAVCustomWriter(stream io.Writer) AVCustomWriter {
	return AVCustomWriter{writer: stream}
}

// Consume writes PPS and SPS as well as sample bufs into a annex b .h264 file and audio samples into a wav file
func (avfw AVCustomWriter) Consume(buf CMSampleBuffer) error {
	if buf.MediaType == MediaTypeSound {
		return nil
	}
	return avfw.consumeVideo(buf)
}

// Nothing currently
func (avfw AVCustomWriter) Stop() {
	// will be firing stop packets with
	// to be done to ensure decoder understands its closing time
}

func (avfw AVCustomWriter) writeVideoHeaderConfig(width uint32, height uint32) []byte {
	// create a buffer of size 8 and send the following put following data to it
	header := make([]byte, 20)
	var pts uint64 = 0
	pts = pts | (1 << 63)
	binary.BigEndian.PutUint64(header[0:8], pts) // PTS n flags
	binary.BigEndian.PutUint32(header[8:12], 0)  // length zero
	binary.BigEndian.PutUint32(header[12:16], width)
	binary.BigEndian.PutUint32(header[16:20], height)
	return header
}

func (avfw AVCustomWriter) composeBufferWithNALu(buf CMSampleBuffer) error {
	// compose a single buffer to get length with nalu and other raw content
	if buf.FormatDescription.VideoDimensionWidth > 0 && buf.FormatDescription.VideoDimensionHeight > 0 {
		// send over writer a config packet with pts -> 0
		header := avfw.writeVideoHeaderConfig(buf.FormatDescription.VideoDimensionWidth, buf.FormatDescription.VideoDimensionHeight)
		_, err := avfw.writer.Write(header)
		if err != nil {
			return nil
		}
	}
	packet := make([]byte, 0)
	isKeyframe := false
	if buf.HasFormatDescription {
		// copy buf.FormatDescription.PPS to packet
		packet = append(packet, startCode...)
		packet = append(packet, buf.FormatDescription.PPS...)
		// copy buf.FormatDescription.SPS to packet
		packet = append(packet, startCode...)
		packet = append(packet, buf.FormatDescription.SPS...)
	}
	if buf.HasSampleData() {
		slice := buf.SampleData
		for len(slice) > 0 {
			length := binary.BigEndian.Uint32(slice)
			isKeyframe = (slice[4] & 0x1F) == 0x05 // Check if the NAL unit is a keyframe
			packet = append(packet, startCode...)
			packet = append(packet, slice[4:length+4]...)
			slice = slice[length+4:]
		}
	}
	header := make([]byte, 12)
	if buf.OutputPresentationTimestamp.CMTimeValue > 17446044073700192000 || buf.OutputPresentationTimestamp.CMTimeValue>>62 > 0 {
		buf.OutputPresentationTimestamp.CMTimeValue = 0
	}
	ptsAndFlags := buf.OutputPresentationTimestamp.CMTimeValue
	if isKeyframe {
		ptsAndFlags = ptsAndFlags | (1 << 62)
	}
	binary.BigEndian.PutUint64(header[0:8], ptsAndFlags) // PTS n flags
	binary.BigEndian.PutUint32(header[8:], uint32(len(packet)))
	_, err := avfw.writer.Write(header)
	if err != nil {
		return err
	}
	_, err = avfw.writer.Write(packet)
	return err
}

func (avfw AVCustomWriter) consumeVideo(buf CMSampleBuffer) error {
	return avfw.composeBufferWithNALu(buf)
}
