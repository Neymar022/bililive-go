package douyin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/tools"
)

var btoolsHTTPClient = &http.Client{Timeout: 30 * time.Second}

var btoolsConsts = struct {
	port      int
	authToken string
}{
	port:      18110,
	authToken: "Basic YTph",
}

type ChannelInfo struct {
	Id     string `json:"id"`
	Title  string `json:"title"`
	Owner  string `json:"owner"`
	Avatar string `json:"avatar"`
	Uid    string `json:"uid"`
}

type liveInfoResp struct {
	Title  string `json:"title"`
	Owner  string `json:"owner"`
	Living bool   `json:"living"`
}

type streamInfoResp struct {
	Stream string `json:"stream"`
}

func NewBtoolsLive(live *Live) btoolsLive {
	return btoolsLive{
		Live:     live,
		roomId:   "",
		hostName: "",
		roomName: "",
	}
}

type btoolsLive struct {
	*Live
	roomId   string
	hostName string
	roomName string
}

func (l *btoolsLive) updateChannelInfo() (err error) {
	var channelInfo ChannelInfo
	channelInfo, err = l.fetchChannelInfo()
	if err != nil {
		return
	}
	if channelInfo.Id == "" {
		err = fmt.Errorf("无法获取频道信息")
		return
	}
	l.hostName = channelInfo.Owner
	l.roomName = channelInfo.Title
	l.roomId = channelInfo.Id
	return
}

func (l *btoolsLive) fetchChannelInfo() (channelInfo ChannelInfo, err error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/bgo/channel-info?url=%s", btoolsConsts.port, url.QueryEscape(l.Url.String()))
	err = requestBTools(endpoint, &channelInfo, 0)
	return
}

func (l *btoolsLive) fetchLiveInfo() (liveInfo liveInfoResp, err error) {
	if l.roomId == "" {
		err = l.updateChannelInfo()
		if err != nil {
			return
		}
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%d/bgo/live-info?platform=douyin&roomId=%s", btoolsConsts.port, url.QueryEscape(l.roomId))
	generation := tools.BToolsGeneration()
	var response struct {
		Title  string `json:"title"`
		Owner  string `json:"owner"`
		Living *bool  `json:"living"`
	}
	if err = requestBTools(endpoint, &response, generation); err != nil {
		return liveInfo, err
	}
	if response.Living == nil {
		return liveInfo, fmt.Errorf("解析服务响应缺少 living，无法确认直播状态")
	}
	tools.ReportBToolsLiveInfo(generation, false)
	return liveInfoResp{Title: response.Title, Owner: response.Owner, Living: *response.Living}, nil
}

func (l *btoolsLive) fetchStreamInfo() (streamInfo streamInfoResp, err error) {
	if l.roomId == "" {
		err = l.updateChannelInfo()
		if err != nil {
			return
		}
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%d/bgo/stream-info?platform=douyin&roomId=%s", btoolsConsts.port, url.QueryEscape(l.roomId))
	err = requestBTools(endpoint, &streamInfo, 0)
	return
}

func requestBTools(endpoint string, result any, generation uint64) error {
	req, reqErr := http.NewRequest(http.MethodGet, endpoint, nil)
	if reqErr != nil {
		return reqErr
	}
	req.Header.Set("Authorization", btoolsConsts.authToken)
	resp, doErr := btoolsHTTPClient.Do(req)
	if doErr != nil {
		return doErr
	}
	defer resp.Body.Close()
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("读取解析服务响应失败: %w", err)
	}
	if len(body) > limit {
		return fmt.Errorf("解析服务响应超出大小限制")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		// 仅透传已知分类，不把上游 Cookie、签名 URL 或错误堆栈回显到 UI。
		if resp.StatusCode == http.StatusInternalServerError && json.Unmarshal(body, &failure) == nil && failure.Error == "所有 API 端点都不可用，请稍后重试" {
			tools.ReportBToolsLiveInfo(generation, true)
			return fmt.Errorf("抖音解析端点持续禁用，等待限频恢复；当前无法确认直播状态")
		}
		return fmt.Errorf("请求失败: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return json.Unmarshal(body, result)
}

func (l *btoolsLive) GetInfo() (info *live.Info, err error) {
	ret := &live.Info{
		Live:     l.Live,
		HostName: l.hostName,
		RoomName: l.roomName,
		Status:   false,
	}

	var liveInfo liveInfoResp
	liveInfo, err = l.fetchLiveInfo()
	if err != nil {
		return
	}
	ret.Status = liveInfo.Living
	ret.HostName = liveInfo.Owner
	ret.RoomName = liveInfo.Title

	return ret, nil
}

func (l *btoolsLive) GetStreamInfos() (us []*live.StreamUrlInfo, err error) {
	if l.roomId == "" {
		err = l.updateChannelInfo()
		if err != nil {
			return
		}
	}
	var streamInfo streamInfoResp
	streamInfo, err = l.fetchStreamInfo()
	if err != nil {
		return
	}
	u, parseErr := url.Parse(streamInfo.Stream)
	if parseErr != nil {
		err = parseErr
		return
	}

	return []*live.StreamUrlInfo{
		{
			Url: u,
		},
	}, nil
}
