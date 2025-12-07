package validator

import (
	"fmt"
	"net/http"
)

// SafeValidate 调用给定的 validator，并且捕获 panic。
// validatorFunc：期望签名 func([]byte, http.Header) bool
// 返回值：bool 表示验证通过与否；err 表示 validator 出现 panic 等异常情况
func SafeValidate(validatorFunc func([]byte, http.Header) (bool, error), respBody []byte, respHeader http.Header) (ok bool, err error) {
	if validatorFunc == nil {
		return true, nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("validator panic: %v", r)
			ok = false
		}
	}()
	ok, err = validatorFunc(respBody, respHeader)
	if !ok && err == nil {
		err = fmt.Errorf("response data verification failed")
	}
	return
}
