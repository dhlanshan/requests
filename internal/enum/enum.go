package enum

type MethodEnum string

const (
	GET     MethodEnum = "GET"
	POST    MethodEnum = "POST"
	PUT     MethodEnum = "PUT"
	PATCH   MethodEnum = "PATCH"
	DELETE  MethodEnum = "DELETE"
	HEAD    MethodEnum = "HEAD"
	OPTIONS MethodEnum = "OPTIONS"
	TRACE   MethodEnum = "TRACE"
	CONNECT MethodEnum = "CONNECT"
)

var MethodList = []MethodEnum{GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS, TRACE, CONNECT}
