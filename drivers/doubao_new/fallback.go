package doubao_new

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// doubaoQs is required on every samantha endpoint. Without it the API answers
// HTTP 200 with an empty body, which is easy to mistake for risk control.
const doubaoQs = "version_code=20800&device_platform=web&aid=497858"

// fallbackLink resolves a playable URL through the old samantha interface.
//
// The Feishu drive API needs DPoP and can be rejected outright (LogoutReason:1 /
// code 4101); the samantha interface only needs the login cookie, so it keeps
// playback working. It returns a direct CDN URL, so it stays a 302 like the
// primary path and never relays bytes through the server.
func (d *DoubaoNew) fallbackLink(ctx context.Context, obj *Object) (*model.Link, error) {
	nodeID := obj.GetID()
	if nodeID == "" {
		return nil, errors.New("missing node_id")
	}

	media, err := d.samanthaPlayInfo(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	target := pickSamanthaURL(media)
	if target == "" {
		return nil, errors.New("no playable transcoded stream")
	}

	headers := http.Header{
		"Referer":    []string{DoubaoURL + "/"},
		"User-Agent": []string{base.UserAgent},
	}
	return &model.Link{URL: target, Header: headers}, nil
}

// samanthaPlayInfo looks up the media key via node_info, since List only keeps
// the Feishu node_token/obj_token and get_play_info requires the TOS key.
func (d *DoubaoNew) samanthaPlayInfo(ctx context.Context, nodeID string) ([]samanthaMedia, error) {
	key, err := d.samanthaMediaKey(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("missing media key")
	}

	var info samanthaPlayInfo
	if err := d.samanthaPost(ctx, "/samantha/media/get_play_info", base.Json{
		"key":     key,
		"node_id": nodeID,
	}, &info); err != nil {
		return nil, err
	}
	if len(info.MediaInfo) == 0 {
		return nil, errors.New("doubao returned no transcoded streams")
	}
	return info.MediaInfo, nil
}

// samanthaMediaKey finds nodeID among its parent's children and returns its key.
// The parent is resolved from the object path so we don't have to guess.
func (d *DoubaoNew) samanthaMediaKey(ctx context.Context, nodeID string) (string, error) {
	parentID := d.samanthaParentID(nodeID)
	var page samanthaPage
	if parentID == "" {
		return "", nil
	}
	if err := d.samanthaPost(ctx, "/samantha/aispace/node_info", base.Json{
		"node_id": parentID,
		"size":    1000,
	}, &page); err != nil {
		return "", err
	}
	for _, n := range page.Children {
		if n.ID == nodeID {
			return n.Key, nil
		}
	}
	return "", nil
}

// samanthaParentID caches the child->parent mapping discovered while listing so
// a seek doesn't have to re-walk the tree.
func (d *DoubaoNew) samanthaParentID(nodeID string) string {
	d.samanthaMu.Lock()
	defer d.samanthaMu.Unlock()
	return d.samanthaParents[nodeID]
}

func (d *DoubaoNew) rememberSamanthaChildren(parentID string, nodes []samanthaNode) {
	if len(nodes) == 0 {
		return
	}
	d.samanthaMu.Lock()
	defer d.samanthaMu.Unlock()
	if d.samanthaParents == nil {
		d.samanthaParents = make(map[string]string, len(nodes))
	}
	for _, n := range nodes {
		if n.ID != "" {
			d.samanthaParents[n.ID] = parentID
		}
	}
}

// samanthaPost issues a samantha call. The API replies with HTTP 200 and an
// empty body when the routing querystring is absent or the caller is throttled,
// so an empty body is reported as an error instead of decoding to a zero value.
func (d *DoubaoNew) samanthaPost(ctx context.Context, path string, body any, out any) error {
	endpoint := DoubaoURL + path + "?" + doubaoQs
	req := base.RestyClient.R()
	req.SetContext(ctx)
	req.SetHeader("accept", "application/json, text/plain, */*")
	req.SetHeader("content-type", "application/json")
	req.SetHeader("origin", DoubaoURL)
	req.SetHeader("referer", DoubaoURL+"/")
	req.SetHeader("agw-js-conv", "str")
	req.SetHeader("user-agent", base.UserAgent)
	if d.Cookie != "" {
		req.SetHeader("cookie", d.Cookie)
	}
	req.SetBody(body)

	res, err := req.Execute(http.MethodPost, endpoint)
	if err != nil {
		return err
	}
	raw := res.Body()
	if len(raw) == 0 {
		return fmt.Errorf("[doubao_new] samantha %s returned an empty body (status: %s)", path, res.Status())
	}
	var common BaseResp
	if err := json.Unmarshal(raw, &common); err != nil {
		return fmt.Errorf("[doubao_new] samantha %s decode failed: %v", path, err)
	}
	if common.Code != 0 {
		msg := common.Msg
		if msg == "" {
			msg = common.Message
		}
		return fmt.Errorf("[doubao_new] samantha %s error (code: %d): %s", path, common.Code, msg)
	}
	if out == nil {
		return nil
	}
	// Responses are wrapped in {"code":0,"data":{...}}; unwrap before decoding
	// into the inner struct.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		raw = envelope.Data
	}
	return json.Unmarshal(raw, out)
}

// pickSamanthaURL returns the highest-resolution stream. Doubao's definition
// labels are relative to the source, so a 4K file's "1080p" can be 2592 wide;
// sorting by meta.width is more reliable than trusting the label.
func pickSamanthaURL(media []samanthaMedia) string {
	best := ""
	bestWidth := -1
	for _, m := range media {
		if m.MainURL == "" {
			continue
		}
		width, _ := strconv.Atoi(m.Meta.Width)
		if width > bestWidth {
			bestWidth = width
			best = m.MainURL
		}
	}
	return best
}

// fallbackList builds a directory listing through the samantha interface.
//
// The Feishu drive API is rejected wholesale ("please log in again", code 5)
// once its token is gone, so browsing stops working even though the login
// cookie is still valid. samantha keeps working with just the cookie, so the
// same fallback that rescues playback also rescues browsing. It is only
// consulted after List has failed on the primary path, so nothing changes
// while the Feishu API is healthy.
func (d *DoubaoNew) fallbackList(ctx context.Context, dir model.Obj) ([]model.Obj, error) {
	parentID := dir.GetID()
	// At the mount root OpenList hands the driver an object whose id is the
	// configured root_folder_id, which is empty for an account root. node_info
	// rejects an empty node_id with code 671010000, so ask homepage for the
	// real root instead.
	if parentID == "" || dir.GetPath() == "" || dir.GetPath() == "/" {
		root, err := d.samanthaRootNode(ctx)
		if err != nil {
			return nil, err
		}
		parentID = root.ID
	}
	if parentID == "" {
		return nil, errors.New("missing node_id")
	}

	nodes, err := d.samanthaChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	d.rememberSamanthaChildren(parentID, nodes)

	objs := make([]model.Obj, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == "" || n.ID == parentID || n.Name == "" {
			continue
		}
		isFolder := n.NodeType == 1
		objs = append(objs, &Object{
			Object: model.Object{
				ID:       n.ID,
				Path:     parentID,
				Name:     n.Name,
				Size:     n.Size,
				IsFolder: isFolder,
			},
			NodeType: n.NodeType,
		})
	}
	// Files listed here have no feishu obj token, so warming them means a
	// samantha play-info round trip; do it now rather than on the first seek.
	d.warmFallbackObjects(objs)
	return objs, nil
}

// samanthaChildren pages through samantha's node_info. Doubao reports the next
// page in next_cursor rather than by repeating the last id we saw, and a
// folder with tens of thousands of videos (影视仓库) needs the whole walk to
// list what the primary interface would have listed.
func (d *DoubaoNew) samanthaChildren(ctx context.Context, parentID string) ([]samanthaNode, error) {
	const pageSize = 1000
	all := make([]samanthaNode, 0, pageSize)
	cursor := ""
	for range 200 {
		body := base.Json{"node_id": parentID, "size": pageSize}
		if cursor != "" {
			body["cursor"] = cursor
		}
		var page samanthaPage
		if err := d.samanthaPost(ctx, "/samantha/aispace/node_info", body, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Children...)
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == "-1" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	return all, nil
}

// samanthaRootNode returns the account root id. It has to come from homepage:
// node_info rejects an empty node_id with code 671010000 ("参数不合法"), and
// the primary listing never reveals the root either, because OpenList asks the
// driver for the root with an object whose id is the configured
// root_folder_id, which is empty for an account root.
func (d *DoubaoNew) samanthaRootNode(ctx context.Context) (samanthaNode, error) {
	if id := d.samanthaRootID(); id != "" {
		return samanthaNode{ID: id, NodeType: 0}, nil
	}
	var home samanthaHome
	if err := d.samanthaPost(ctx, "/samantha/aispace/homepage", base.Json{}, &home); err != nil {
		return samanthaNode{}, err
	}
	rootID := home.RootID
	if rootID == "" && len(home.Children) > 0 {
		rootID = home.Children[0].ParentID
	}
	if rootID == "" {
		return samanthaNode{}, errors.New("samantha did not report a root node")
	}
	d.rememberSamanthaRoot(rootID)
	return samanthaNode{ID: rootID, NodeType: 0}, nil
}

func (d *DoubaoNew) samanthaRootID() string {
	d.samanthaMu.Lock()
	defer d.samanthaMu.Unlock()
	return d.samanthaRoot
}

func (d *DoubaoNew) rememberSamanthaRoot(id string) {
	if id == "" {
		return
	}
	d.samanthaMu.Lock()
	defer d.samanthaMu.Unlock()
	d.samanthaRoot = id
}
