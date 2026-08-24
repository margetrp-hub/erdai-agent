package main

// botgo logs credentials and access tokens at debug level. Connector health is
// tracked by Core, so the SDK logger stays silent instead of risking disclosure.
type quietQQSDKLogger struct{}

type qqSDKHealthLogger struct {
	onReceive      func(string)
	onHandlerError func(error)
}

func (l qqSDKHealthLogger) Debug(...interface{}) {}
func (l qqSDKHealthLogger) Info(...interface{})  {}
func (l qqSDKHealthLogger) Warn(...interface{})  {}
func (l qqSDKHealthLogger) Error(...interface{}) {}
func (l qqSDKHealthLogger) Debugf(string, ...interface{}) {
}
func (l qqSDKHealthLogger) Infof(format string, values ...interface{}) {
	if format != "%s receive %s message, %s" || len(values) < 2 || l.onReceive == nil {
		return
	}
	opcode, ok := values[1].(string)
	if ok {
		l.onReceive(opcode)
	}
}
func (l qqSDKHealthLogger) Warnf(string, ...interface{}) {}
func (l qqSDKHealthLogger) Errorf(format string, values ...interface{}) {
	if format != "%s parseAndHandle failed, %v" || len(values) < 2 || l.onHandlerError == nil {
		return
	}
	if err, ok := values[1].(error); ok {
		l.onHandlerError(err)
	}
}
func (l qqSDKHealthLogger) Sync() error { return nil }

func (quietQQSDKLogger) Debug(...interface{})          {}
func (quietQQSDKLogger) Info(...interface{})           {}
func (quietQQSDKLogger) Warn(...interface{})           {}
func (quietQQSDKLogger) Error(...interface{})          {}
func (quietQQSDKLogger) Debugf(string, ...interface{}) {}
func (quietQQSDKLogger) Infof(string, ...interface{})  {}
func (quietQQSDKLogger) Warnf(string, ...interface{})  {}
func (quietQQSDKLogger) Errorf(string, ...interface{}) {}
func (quietQQSDKLogger) Sync() error                   { return nil }
