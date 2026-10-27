// Package httpapi 提供书源实例的 HTTP 接入层（基于 echo）。
package httpapi

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legado-go/internal/model"
)

// ErrorBody 是统一错误响应
type ErrorBody struct {
	Error Detail `json:"error"`
}

// Detail 是错误细节
type Detail struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// StatusByKind 把错误种类映射为 HTTP 状态码
func StatusByKind(kind string) int {
	switch kind {
	case model.KindInvalidConfig:
		return http.StatusBadRequest
	case model.KindNotFound:
		return http.StatusNotFound
	case model.KindExpired, model.KindClosed:
		return http.StatusGone
	case model.KindLimit:
		return http.StatusTooManyRequests
	case model.KindNetwork, model.KindHTTP:
		return http.StatusBadGateway
	case model.KindTimeout:
		return http.StatusGatewayTimeout
	case model.KindParseFailed, model.KindParseEmpty:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// fail 生成错误响应
func fail(err error) *echo.HTTPError {
	kind := model.KindOf(err)
	msg := err.Error()
	var me *model.Error
	if errors.As(err, &me) {
		msg = me.Msg
		if me.Cause != nil {
			msg = msg + ": " + me.Cause.Error()
		}
	}
	return echo.NewHTTPError(StatusByKind(kind), ErrorBody{Error: Detail{Kind: kind, Message: msg}})
}

// respondError 写出错误响应
func respondError(c echo.Context, err error) error {
	return fail(err)
}
