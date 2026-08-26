package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	model "project/internal/model"
	query "project/internal/query"
	utils "project/pkg/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const operationLogMessageLimit = 10000

var (
	sensitiveAssignmentPattern = regexp.MustCompile(`(?i)((?:password|passwd|pwd|token|api[_-]?key|authorization|voucher|secret|private[_-]?key|credential|jwt)"?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,&}]+)`)
	bearerTokenPattern         = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
)

func OperationLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isModifyMethod(c.Request.Method) {
			c.Next()
			return
		}

		logrus.Info("开始处理请求:", c.Request.URL.Path, "方法:", c.Request.Method)
		requestMessage := processRequestBody(c)
		// 对于文件上传请求，不打印完整请求体（包含二进制内容）
		if !isMultipartRequest(c) {
			logrus.Info("请求体:", requestMessage)
		} else {
			logrus.Info("请求体: [文件上传请求，已过滤二进制内容]", requestMessage)
		}

		writer := newResponseBodyWriter(c)
		c.Writer = writer

		start := time.Now().UTC()
		c.Next()
		cost := time.Since(start).Milliseconds()

		logrus.Info("请求处理完成，状态码:", c.Writer.Status(), "耗时(ms):", cost)
		logrus.Info("响应体大小:", writer.body.Len())
		responseMessage := redactOperationLogMessage(writer.body.String())
		logrus.Info("响应的信息:", responseMessage)

		saveOperationLog(c, start, cost, requestMessage, responseMessage)
	}
}

func isModifyMethod(method string) bool {
	return method == http.MethodPost ||
		method == http.MethodPut ||
		method == http.MethodDelete
}

func processRequestBody(c *gin.Context) string {
	// 检查是否是 multipart/form-data 请求（文件上传）
	if isMultipartRequest(c) {
		// 对于 multipart 请求，不读取请求体（包含二进制文件内容）
		// 只记录路径信息，避免消耗请求体导致后续无法读取文件
		return fmt.Sprintf("[文件上传请求: %s]", c.Request.URL.Path)
	}

	// 对于普通请求，读取请求体
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logrus.Error("读取请求体失败:", err)
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	return redactOperationLogMessage(string(body))
}

func redactOperationLogMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}

	var value interface{}
	if json.Unmarshal([]byte(message), &value) == nil {
		redactSensitiveValues(value)
		if encoded, err := json.Marshal(value); err == nil {
			message = string(encoded)
		}
	} else {
		message = bearerTokenPattern.ReplaceAllString(message, `${1}[REDACTED]`)
		message = sensitiveAssignmentPattern.ReplaceAllString(message, `${1}"[REDACTED]"`)
	}

	if len(message) > operationLogMessageLimit {
		return message[:operationLogMessageLimit] + "...[内容过长已截断]"
	}
	return message
}

func redactSensitiveValues(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			normalizedKey := strings.Map(func(r rune) rune {
				if r >= 'A' && r <= 'Z' {
					return r + ('a' - 'A')
				}
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
					return r
				}
				return -1
			}, key)
			if strings.Contains(normalizedKey, "password") || strings.Contains(normalizedKey, "passwd") ||
				normalizedKey == "pwd" || strings.Contains(normalizedKey, "token") ||
				strings.Contains(normalizedKey, "apikey") || strings.Contains(normalizedKey, "accesskey") ||
				strings.Contains(normalizedKey, "authorization") ||
				strings.Contains(normalizedKey, "voucher") || strings.Contains(normalizedKey, "secret") ||
				strings.Contains(normalizedKey, "privatekey") || strings.Contains(normalizedKey, "credential") ||
				normalizedKey == "jwt" {
				typed[key] = "[REDACTED]"
				continue
			}
			redactSensitiveValues(child)
		}
	case []interface{}:
		for _, child := range typed {
			redactSensitiveValues(child)
		}
	}
}

// isMultipartRequest 检查是否是 multipart/form-data 请求
func isMultipartRequest(c *gin.Context) bool {
	contentType := c.Request.Header.Get("Content-Type")
	return strings.HasPrefix(contentType, "multipart/form-data")
}

func saveOperationLog(c *gin.Context, start time.Time, cost int64, requestMsg, responseMsg string) {
	// 检查 claims 是否存在
	claims, exists := c.Get("claims")
	if !exists {
		logrus.Info("未找到用户信息，跳过操作日志记录")
		return
	}

	// 类型断言
	userClaims, ok := claims.(*utils.UserClaims)
	if !ok {
		logrus.Info("用户信息类型不正确，跳过操作日志记录")
		return
	}

	// 检查 tenantID 是否为空
	if userClaims.TenantID == "" {
		logrus.Info("租户ID为空，跳过操作日志记录")
		return
	}

	path := c.Request.URL.Path

	log := &model.OperationLog{
		ID:              uuid.NewString(),
		IP:              c.ClientIP(),
		Path:            &path,
		UserID:          userClaims.ID,
		Name:            &c.Request.Method,
		CreatedAt:       start,
		Latency:         &cost,
		RequestMessage:  &requestMsg,
		ResponseMessage: &responseMsg,
		TenantID:        userClaims.TenantID,
	}

	query.OperationLog.Create(log)
}

type responseBodyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func newResponseBodyWriter(c *gin.Context) responseBodyWriter {
	return responseBodyWriter{
		ResponseWriter: c.Writer,
		body:           &bytes.Buffer{},
	}
}

func (r responseBodyWriter) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
