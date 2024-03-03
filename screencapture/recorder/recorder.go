package recorder

/*
#cgo pkg-config: libusb-1.0
#include <libusb.h>
*/
import "C"
import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/LambdatestIncPrivate/ios-quicktime-capture/screencapture"
	"github.com/LambdatestIncPrivate/ios-quicktime-capture/screencapture/coremedia"
	"github.com/LambdatestIncPrivate/ios-quicktime-capture/screencapture/decoder"
	"github.com/sirupsen/logrus"
)

type Recorder struct {
	libUsbCtx     *C.libusb_context
	ctx           context.Context
	cancel        context.CancelFunc
	logger        *logrus.Logger
	wg            sync.WaitGroup
	device        screencapture.IosDevice
	quickTimeMode bool
}

func NewRecorder(logger *logrus.Logger, quickTimeMode bool) *Recorder {
	r := &Recorder{logger: logger, quickTimeMode: quickTimeMode}
	if quickTimeMode {
		C.libusb_init(&r.libUsbCtx)
	}
	return r
}

func (r *Recorder) Close() {
	r.cancel()
	r.wg.Wait()
	time.Sleep(1 * time.Second) // added on purpose to keep the program running until mp4 is generated
	if r.quickTimeMode {
		C.libusb_exit(r.libUsbCtx)
	}
}

func (r *Recorder) ConfigureDevice(udid string) error {
	device, err := findDevice(udid)
	r.device = device
	if err != nil {
		return errors.New("no such device found")
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.wg = sync.WaitGroup{}
	r.deactivate()
	return r.activate()
}

func (r *Recorder) deactivate() {
	r.logger.Debugf("Disabling device: %v", r.device)
	var err error
	r.device, err = screencapture.DisableQTConfig(r.device)
	if err != nil {
		r.logger.Errorf("Error disabling QT config: %v", err)
		return
	}
}

// findDevice grabs the first device on the host for a empty --udid
// or tries to find the provided device otherwise
func findDevice(udid string) (screencapture.IosDevice, error) {
	if udid == "" {
		return screencapture.FindIosDevice("")
	}
	usbSerial, err := screencapture.ValidateUdid(udid)
	if err != nil {
		return screencapture.IosDevice{}, err
	}

	return screencapture.FindIosDevice(usbSerial)
}

// This command is for testing if we can enable the hidden Quicktime device config
func (r *Recorder) activate() error {
	r.logger.Debugf("Enabling device: %v", r.device)
	var err error
	device, err := screencapture.EnableQTConfig(r.device)
	if err != nil {
		r.logger.Errorf("Error enabling QT config: %v", err)
		return err
	}
	r.device = device
	return nil
}

func (r *Recorder) RecordViaDevice(port int, address, outPath string) error {
	decoder := decoder.NewDecoder(r.ctx, address, port, r.logger)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		err := decoder.Decode(outPath)
		if err != nil {
			r.logger.Errorf("error decoding: %v", err)
		}
		r.logger.Infof("Decoder stopped")
	}()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		<-r.ctx.Done() // Blocks until the context is cancelled.
		r.logger.Infof("Context cancelled, stopping server...")
	}()
	return nil
}
func (r *Recorder) Record(port int, outPath string) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		r.logger.Errorf("unable to start tcp server at port %d: %v", port, err)
		return err
	}
	r.logger.Infof("Server listening on port %d", port)
	decoder := decoder.NewDecoder(r.ctx, "", port, r.logger)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		err := decoder.Decode(outPath)
		if err != nil {
			r.logger.Errorf("error decoding: %v", err)
		}
	}()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		<-r.ctx.Done() // Blocks until the context is cancelled.
		r.logger.Infof("Context cancelled, stopping server...")
		ln.Close() // This will cause ln.Accept() to return an error.
	}()
	go func() {
		for {
			conn, err := ln.Accept()

			if err != nil {
				// Check if the error is because of the listener being closed.
				select {
				case <-r.ctx.Done():
					r.logger.Infof("Server stopped due to context cancellation.")
					return
				default:
					r.logger.Errorf("Error accepting connection: %s", err)
				}
				return
			}
			// Handle the connection in a new goroutine.
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer conn.Close()
				writer := coremedia.NewAVCustomWriter(conn)
				r.startWithConsumer(writer)
			}()
		}
	}()
	return nil

}

func (r *Recorder) startWithConsumer(consumer screencapture.CmSampleBufConsumer) error {
	adapter := screencapture.UsbAdapter{}
	stopSignal := make(chan interface{})
	r.waitForSigInt(stopSignal)
	mp := screencapture.NewMessageProcessor(&adapter, stopSignal, consumer, true)
	err := adapter.StartReading(r.device, &mp, stopSignal)
	consumer.Stop()
	return err
}

func (r *Recorder) waitForSigInt(stopSignalChannel chan interface{}) {

	go func() {

		<-r.ctx.Done()
		var stopSignal interface{}
		stopSignalChannel <- stopSignal
	}()
}
