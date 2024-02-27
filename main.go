package main

/*
#cgo pkg-config: libusb-1.0
#include <libusb.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LambdatestIncPrivate/ios-quicktime-capture/screencapture"
	"github.com/LambdatestIncPrivate/ios-quicktime-capture/screencapture/coremedia"
	"github.com/LambdatestIncPrivate/ios-quicktime-capture/screencapture/decoder"
	log "github.com/sirupsen/logrus"
)

const version = "v0.6-beta"

func main() {
	var ctx *C.libusb_context
	C.libusb_init(&ctx)
	defer C.libusb_exit(ctx)

	device, err := findDevice("")
	if err != nil {
		printErrJSON(err, "no device found to use")
		return
	}
	deactivate(device)
	activate(device)
	ctxWithCancel, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure any resources are cleaned up properly

	// Listen for SIGINT
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT)
	go func() {
		<-sigint
		cancel() // Cancel the context when SIGINT is received
	}()

	record(ctxWithCancel, device)
	// recordViaDevice(ctxWithCancel)
	// sleep aded on purpose to keep the program running until mp4
	time.Sleep(5 * time.Second)

}

func deactivate(device screencapture.IosDevice) {
	log.Debugf("Disabling device: %v", device)
	var err error
	device, err = screencapture.DisableQTConfig(device)
	if err != nil {
		printErrJSON(err, "Error disabling QT config")
		return
	}

	printJSON(map[string]interface{}{
		"device_activated": device.DetailsMap(),
	})
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
	log.Debugf("requested usb-serial:'%s' from udid:%s", usbSerial, udid)

	return screencapture.FindIosDevice(usbSerial)
}

// This command is for testing if we can enable the hidden Quicktime device config
func activate(device screencapture.IosDevice) {
	log.Debugf("Enabling device: %v", device)
	var err error
	device, err = screencapture.EnableQTConfig(device)
	if err != nil {
		printErrJSON(err, "Error enabling QT config")
		return
	}

	printJSON(map[string]interface{}{
		"device_activated": device.DetailsMap(),
	})
}

func recordViaDevice(ctx context.Context) {

	decoder := decoder.NewDecoder(ctx, 9009)
	// go func() {
	err := decoder.Decode("/Users/prncvrm/gowork/src/github.com/LambdatestIncPrivate/ios-quicktime-capture/output.mp4")
	if err != nil {
		printErrJSON(err, "Error decoding")
	}
	// }()
	go func() {
		<-ctx.Done() // Blocks until the context is cancelled.
		log.Printf("Context cancelled, stopping server...")
	}()
}
func record(ctx context.Context, device screencapture.IosDevice) {
	port := "9009"
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("Error starting server on port %s: %s", port, err)
	}
	defer ln.Close()

	log.Printf("Server listening on port %s", port)

	decoder := decoder.NewDecoder(ctx, 9009)
	go func() {
		err := decoder.Decode("/Users/prncvrm/gowork/src/github.com/LambdatestIncPrivate/ios-quicktime-capture/output.mp4")
		if err != nil {
			printErrJSON(err, "Error decoding")
		}
	}()
	go func() {
		<-ctx.Done() // Blocks until the context is cancelled.
		log.Printf("Context cancelled, stopping server...")
		ln.Close() // This will cause ln.Accept() to return an error.
	}()
	for {
		conn, err := ln.Accept()

		if err != nil {
			// Check if the error is because of the listener being closed.
			select {
			case <-ctx.Done():
				log.Printf("Server stopped due to context cancellation.")
			default:
				log.Printf("Error accepting connection: %s", err)
			}
			return // Stop the loop and end the function.
		}
		log.Printf("Client connected from %s", conn.RemoteAddr().String())

		// Handle the connection in a new goroutine.
		go handleConnection(conn, device)
	}

}

func handleConnection(conn net.Conn, device screencapture.IosDevice) {
	defer conn.Close()
	writer := coremedia.NewAVCustomWriter(conn)
	// Assuming startWithConsumer initiates the screen capture and writes to the provided writer
	startWithConsumer(writer, device, false)
}

func startWithConsumer(consumer screencapture.CmSampleBufConsumer, device screencapture.IosDevice, audioOnly bool) {
	var err error
	device, err = screencapture.EnableQTConfig(device)
	if err != nil {
		printErrJSON(err, "Error enabling QT config")
		return
	}

	adapter := screencapture.UsbAdapter{}
	stopSignal := make(chan interface{})
	waitForSigInt(stopSignal)

	mp := screencapture.NewMessageProcessor(&adapter, stopSignal, consumer, audioOnly)

	err = adapter.StartReading(device, &mp, stopSignal)
	consumer.Stop()
	if err != nil {
		printErrJSON(err, "failed connecting to usb")
	}
}

func waitForSigInt(stopSignalChannel chan interface{}) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for sig := range c {
			log.Debugf("Signal received: %s", sig)
			var stopSignal interface{}
			stopSignalChannel <- stopSignal
		}
	}()
}

func printErrJSON(err error, msg string) {
	printJSON(map[string]interface{}{
		"original_error": err.Error(),
		"error_message":  msg,
	})
}
func printJSON(output map[string]interface{}) {
	text, err := json.Marshal(output)
	if err != nil {
		log.Fatalf("Broken json serialization, error: %s", err)
	}
	println(string(text))
}

// this is to ban these irritating "2021/04/29 14:27:59 handle_events: error: libusb: interrupted [code -10]" libusb messages
type LogrusWriter int

const interruptedError = "interrupted [code -10]"

func (LogrusWriter) Write(data []byte) (int, error) {
	logmessage := string(data)
	if strings.Contains(logmessage, interruptedError) {
		log.Tracef("gousb_logs:%s", logmessage)
		return len(data), nil
	}
	log.Infof("gousb_logs:%s", logmessage)
	return len(data), nil
}
