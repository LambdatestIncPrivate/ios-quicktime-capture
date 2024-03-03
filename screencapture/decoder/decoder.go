package decoder

/*
#cgo pkg-config: libavformat libavcodec libavutil libavfilter
#include <libavutil/avutil.h>
#include "decoder.h"

*/
import "C"
import (
	"context"
	"fmt"
	"unsafe"

	"github.com/sirupsen/logrus"
)

var logger *logrus.Entry

//export GoLoggingCallback
func GoLoggingCallback(msg *C.char) {
	goMsg := C.GoString(msg)
	if logger != nil {
		logger.Info(goMsg)
	} else {
		fmt.Println(goMsg)
	}
}

type Decoder struct {
	ctx        context.Context
	address    string
	portNumber int
}

func NewDecoder(ctx context.Context, address string, portNumber int, log *logrus.Logger) *Decoder {
	if address == "" {
		address = "127.0.0.1"
	}
	logger = log.WithFields(logrus.Fields{"module": "decoder", "address": address, "port": portNumber, "service": "ios-quicktime-capture"})
	return &Decoder{ctx: ctx, address: address, portNumber: portNumber}
}

func (d *Decoder) Decode(outputFilename string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("panic occurred in C.convert_to_mp4: %v", r)
			err = fmt.Errorf("panic occurred in C.convert_to_mp4: %v", r)
		}
	}()
	outputFilenameC := C.CString(outputFilename)
	defer C.free(unsafe.Pointer(outputFilenameC))
	go func() {
		logger.Info("waiting for context to be done")
		<-d.ctx.Done()
		d.cancel()
		logger.Info("context cancelled")
	}()
	ret := C.convert_to_mp4(outputFilenameC, C.uint32_t(d.portNumber), C.CString(d.address))
	if ret != 0 {
		logger.Errorf("error converting to mp4, exit status not zero")
		return fmt.Errorf("error converting to mp4, exit status not zero")
	}
	logger.Info("mp4 conversion completed")
	return nil
}

func (d *Decoder) cancel() {
	C.set_cancelled(true)
}
