package server

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

// testContext 给直接调处理函数的测试造一个 gin.Context；params 是成对的路径参数名与值。
func testContext(w http.ResponseWriter, r *http.Request, params ...string) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	c.Request = r
	for i := 0; i+1 < len(params); i += 2 {
		c.Params = append(c.Params, gin.Param{Key: params[i], Value: params[i+1]})
	}
	return c
}

// testJSON 给测试里的假上游用：写一个 JSON 响应。
func testJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
