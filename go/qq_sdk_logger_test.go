package main

import (
	"errors"
	"testing"
)

type panicQQLogValue struct{}

func (panicQQLogValue) String() string { panic("QQ SDK logger formatted a secret") }

func TestQQSDKLoggerNeverFormatsCredentials(t *testing.T) {
	logger := quietQQSDKLogger{}
	secret := panicQQLogValue{}
	logger.Debug(secret)
	logger.Info(secret)
	logger.Warn(secret)
	logger.Error(secret)
	logger.Debugf("%v", secret)
	logger.Infof("%v", secret)
	logger.Warnf("%v", secret)
	logger.Errorf("%v", secret)
	if err := logger.Sync(); err != nil {
		t.Fatal(err)
	}
}

func TestQQSDKHealthLoggerObservesInboundWithoutFormattingPayload(t *testing.T) {
	var opcode string
	var handlerErr error
	logger := qqSDKHealthLogger{
		onReceive:      func(value string) { opcode = value },
		onHandlerError: func(err error) { handlerErr = err },
	}
	secret := panicQQLogValue{}
	logger.Infof("%s receive %s message, %s", secret, "HeartbeatAck", secret)
	if opcode != "HeartbeatAck" {
		t.Fatalf("observed opcode = %q", opcode)
	}
	logger.Infof("attacker-controlled %s", secret)
	wantErr := errors.New("group event parse failed")
	logger.Errorf("%s parseAndHandle failed, %v", secret, wantErr)
	if !errors.Is(handlerErr, wantErr) {
		t.Fatalf("handler error = %v", handlerErr)
	}
	logger.Errorf("attacker-controlled %s", secret)
}
