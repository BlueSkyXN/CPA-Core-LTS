package pluginhost

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func requestInterceptorCall(method string, interceptor pluginapi.RequestInterceptor) (func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error), bool) {
	if interceptor == nil {
		return nil, false
	}
	switch method {
	case "RequestInterceptor.InterceptRequestBeforeAuth":
		if staged, ok := interceptor.(pluginapi.RequestInterceptorBeforeAuth); ok {
			return staged.InterceptRequestBeforeAuth, true
		}
		if legacy, ok := interceptor.(pluginapi.LegacyRequestInterceptor); ok {
			return legacy.InterceptRequest, true
		}
	case "RequestInterceptor.InterceptRequestAfterAuth":
		if staged, ok := interceptor.(pluginapi.RequestInterceptorAfterAuth); ok {
			return staged.InterceptRequestAfterAuth, true
		}
	}
	return nil, false
}

func hasRequestInterceptorCapability(interceptor pluginapi.RequestInterceptor) bool {
	_, beforeAuth := requestInterceptorCall("RequestInterceptor.InterceptRequestBeforeAuth", interceptor)
	if beforeAuth {
		return true
	}
	_, afterAuth := requestInterceptorCall("RequestInterceptor.InterceptRequestAfterAuth", interceptor)
	return afterAuth
}
