package cache

import (
	"net/http"
	"time"

	"github.com/apache/incubator-trafficcontrol/grove/cachedata"
	"github.com/apache/incubator-trafficcontrol/grove/plugin"
	"github.com/apache/incubator-trafficcontrol/grove/remapdata"
	"github.com/apache/incubator-trafficcontrol/grove/stat"
	"github.com/apache/incubator-trafficcontrol/grove/web"

	"github.com/apache/incubator-trafficcontrol/lib/go-log"
)

// Responder is an object encapsulating the cache's response to the client. It holds all the data necessary to respond, log the response, and add the stats.
type Responder struct {
	W            http.ResponseWriter
	PluginCfg    map[string]interface{}
	Plugins      plugin.Plugins
	Stats        stat.Stats
	F            RespondFunc
	ResponseCode *int
	cachedata.ParentRespData
	cachedata.SrvrData
	cachedata.ReqData
}

func DefaultParentRespData() cachedata.ParentRespData {
	return cachedata.ParentRespData{
		Reuse:               remapdata.ReuseCannot,
		OriginCode:          0,
		OriginReqSuccess:    false,
		OriginConnectFailed: false,
		OriginBytes:         0,
		ProxyStr:            "-",
	}
}

func DefaultRespCode() *int {
	c := http.StatusBadRequest
	return &c
}

type RespondFunc func() (uint64, error)

// NewResponder creates a Responder, which defaults to a generic error response.
func NewResponder(w http.ResponseWriter, pluginCfg map[string]interface{}, srvrData cachedata.SrvrData, reqData cachedata.ReqData, plugins plugin.Plugins, stats stat.Stats) *Responder {
	responder := &Responder{
		W:              w,
		PluginCfg:      pluginCfg,
		Plugins:        plugins,
		Stats:          stats,
		ResponseCode:   DefaultRespCode(),
		ParentRespData: DefaultParentRespData(),
		SrvrData:       srvrData,
		ReqData:        reqData,
	}
	responder.F = func() (uint64, error) { return web.ServeErr(w, *responder.ResponseCode) }
	return responder
}

// SetResponse is a helper which sets the RespondFunc of r to `web.Respond` with the given code, headers, body, and connectionClose. Note it takes a pointer to the headers and body, which may be modified after calling this but before the Do() sends the response.
func (r *Responder) SetResponse(code *int, hdrs *http.Header, body *[]byte, connectionClose bool) {
	r.ResponseCode = code
	r.F = func() (uint64, error) { return web.Respond(r.W, *code, *hdrs, *body, connectionClose) }
}

// Do responds to the client, according to the data in r, with the given code, headers, and body. It additionally writes to the event log, and adds statistics about this request. This should always be called for the final response to a client, in order to properly log, stat, and other final operations.
// For cache misses, reuse should be ReuseCannot.
// For parent connect failures, originCode should be 0.
func (r *Responder) Do() {
	// TODO move plugins.BeforeRespond here? How do we distinguish between success, and know to set headers? r.OriginReqSuccess?
	bytesSent, err := r.F()
	if err != nil {
		log.Errorln(time.Now().Format(time.RFC3339Nano) + " " + r.Req.RemoteAddr + " " + r.Req.Method + " " + r.Req.RequestURI + ": responding: " + err.Error())
	}
	web.TryFlush(r.W) // TODO remove? Let plugins do it, if they need to?

	respSuccess := err != nil
	respData := cachedata.RespData{*r.ResponseCode, bytesSent, respSuccess, isCacheHit(r.Reuse, r.OriginCode)}
	arData := plugin.AfterRespondData{r.W, r.Stats, r.ReqData, r.SrvrData, r.ParentRespData, respData}
	r.Plugins.OnAfterRespond(r.PluginCfg, arData)
}

func isCacheHit(reuse remapdata.Reuse, originCode int) bool {
	// TODO move to web? remap?
	return reuse == remapdata.ReuseCan || ((reuse == remapdata.ReuseMustRevalidate || reuse == remapdata.ReuseMustRevalidateCanStale) && originCode == http.StatusNotModified)
}