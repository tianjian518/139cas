package common

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"maps"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// SetPreconnectHeader asks the client to open a connection to the redirect
// target ahead of time, so the next seek does not pay TCP+TLS setup inside its
// critical path. This is a hint only: the redirect stays a plain 302 and not a
// single byte of the media passes through this server.
//
// Browsers only honour preconnect for requests without credentials (the fetch
// spec's "credentials" preconnect mode), which anonymous CORS requests use, so
// an unqualified preconnect would be ignored for a fetch-based player. The
// origin is therefore emitted once as anonymous=1 and once as bare
// crossorigin, which is the mode a <video src> media request actually uses.
// NoCacheRedirect must be called alongside it, otherwise a cache can replay an
// already-used signature.
func SetPreconnectHeader(w http.ResponseWriter, target string) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	origin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	w.Header().Add("Link", fmt.Sprintf("<%s>; rel=preconnect; anonymous=1", origin))
	w.Header().Add("Link", fmt.Sprintf("<%s>; rel=preconnect; crossorigin", origin))
}

// NoCacheRedirect marks a 302 so that no client or proxy stores it.
//
// It used to answer "public, max-age=86400". That saved the hop on repeated
// seeks, but on the Feishu CDN a stale redirect is worse than a fresh one: the
// DPoP proof inside the signed URL has a one-hour life while the URL itself is
// good for days, so a player that replays a cached 302 from an earlier session
// seeks to a URL the CDN has already dropped. The redirect is signed per request
// and costs under a millisecond to reissue, and it is never followed by the
// server, so revalidating it is free. Players that issue several range requests
// at once therefore always get a live URL, not a replay.
func NoCacheRedirect(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	// The Location is signed, i.e. specific to the requesting client. No shared
	// cache in between may ever reuse it.
	w.Header().Set("Vary", "Referer")
}

func Proxy(w http.ResponseWriter, r *http.Request, link *model.Link, file model.Obj) error {
	// if link.MFile != nil {
	// 	attachHeader(w, file, link)
	// 	http.ServeContent(w, r, file.GetName(), file.ModTime(), link.MFile)
	// 	return nil
	// }

	if link.Concurrency > 0 || link.PartSize > 0 {
		attachHeader(w, file, link)
		size := link.ContentLength
		if size <= 0 {
			size = file.GetSize()
		}
		rrf, _ := stream.GetRangeReaderFromLink(size, link)
		if link.RangeReader == nil {
			r = r.WithContext(context.WithValue(r.Context(), conf.RequestHeaderKey, r.Header))
		}
		return net.ServeHTTP(w, r, file.GetName(), file.ModTime(), size, &model.RangeReadCloser{
			RangeReader: rrf,
		})
	}

	if link.RangeReader != nil {
		attachHeader(w, file, link)
		size := link.ContentLength
		if size <= 0 {
			size = file.GetSize()
		}
		return net.ServeHTTP(w, r, file.GetName(), file.ModTime(), size, &model.RangeReadCloser{
			RangeReader: link.RangeReader,
		})
	}

	//transparent proxy
	header := net.ProcessHeader(r.Header, link.Header)
	res, err := net.RequestHttp(r.Context(), r.Method, header, link.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	maps.Copy(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = utils.CopyWithBuffer(w, &stream.RateLimitReader{
		Reader:  res.Body,
		Limiter: stream.ServerDownloadLimit,
		Ctx:     r.Context(),
	})
	return err
}
func attachHeader(w http.ResponseWriter, file model.Obj, link *model.Link) {
	fileName := file.GetName()
	w.Header().Set("Content-Disposition", utils.GenerateContentDisposition(fileName))
	w.Header().Set("Content-Type", utils.GetMimeType(fileName))
	size := link.ContentLength
	if size <= 0 {
		size = file.GetSize()
	}
	w.Header().Set("Etag", GetEtag(file, size))
	contentType := link.Header.Get("Content-Type")
	if len(contentType) > 0 {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", utils.GetMimeType(fileName))
	}
}
func GetEtag(file model.Obj, size int64) string {
	hash := ""
	for _, v := range file.GetHash().Export() {
		if v > hash {
			hash = v
		}
	}
	if len(hash) > 0 {
		return fmt.Sprintf(`"%s"`, hash)
	}
	// 参考nginx
	return fmt.Sprintf(`"%x-%x"`, file.ModTime().Unix(), size)
}

func ProxyRange(ctx context.Context, link *model.Link, size int64) *model.Link {
	if link.RangeReader == nil && !strings.HasPrefix(link.URL, GetApiUrl(ctx)+"/") {
		if link.ContentLength > 0 {
			size = link.ContentLength
		}
		rrf, err := stream.GetRangeReaderFromLink(size, link)
		if err == nil {
			return &model.Link{
				RangeReader:   rrf,
				ContentLength: size,
			}
		}
	}
	return link
}

type InterceptResponseWriter struct {
	http.ResponseWriter
	io.Writer
}

func (iw *InterceptResponseWriter) Write(p []byte) (int, error) {
	return iw.Writer.Write(p)
}

type WrittenResponseWriter struct {
	http.ResponseWriter
	written bool
}

func (ww *WrittenResponseWriter) Write(p []byte) (int, error) {
	n, err := ww.ResponseWriter.Write(p)
	if !ww.written && n > 0 {
		ww.written = true
	}
	return n, err
}

func (ww *WrittenResponseWriter) IsWritten() bool {
	return ww.written
}

func GenerateDownProxyURL(storage *model.Storage, reqPath string) string {
	if storage.DownProxyURL == "" {
		return ""
	}
	query := ""
	if !storage.DisableProxySign {
		query = "?sign=" + sign.Sign(reqPath)
	}
	return fmt.Sprintf("%s%s%s",
		strings.Split(storage.DownProxyURL, "\n")[0],
		utils.EncodePath(reqPath, true),
		query,
	)
}
