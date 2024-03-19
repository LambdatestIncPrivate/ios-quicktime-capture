package decoder

import (
	"context"

	"github.com/sirupsen/logrus"
)

var logger *logrus.Entry

type DecoderService interface {
	Decode(outputFilename string) (err error)
	cancel()
}

func NewDecoder(ctx context.Context, address string, portNumber int, log *logrus.Logger) DecoderService {
	if address == "" {
		address = "127.0.0.1"
	}
	logger = log.WithFields(logrus.Fields{"module": "decoder", "address": address, "port": portNumber, "service": "ios-quicktime-capture"})
	return &Decoder{ctx: ctx, address: address, portNumber: portNumber}
}
