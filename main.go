package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
  "net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/joho/godotenv"
)

const CURRENT_VERSION = "3.0.1"

const (
	AnilistUrl      = "https://graphql.anilist.co"
	MiruroPipeUrl   = "https://www.miruro.tv/api/secure/pipe"
	MediaListFields = `
    id
    title { romaji english native }
    coverImage { large extraLarge }
    bannerImage
    format
    season
    seasonYear
    episodes
    duration
    status
    averageScore
    meanScore
    popularity
    favourites
    genres
    source
    countryOfOrigin
    isAdult
    studios { edges { isMain node { name isAnimationStudio } } }
    nextAiringEpisode { episode airingAt timeUntilAiring }
    startDate { year month day }
    endDate { year month day }
    description
    tags { name isGeneralSpoiler isAdult}
    externalLinks { url site type }
    trailer { id site thumbnail }
`
	MediaFullFields = `
    id
    idMal
    title { romaji english native }
    description(asHtml: false)
    coverImage { large extraLarge color }
    bannerImage
    format
    season
    seasonYear
    episodes
    duration
    status
    averageScore
    meanScore
    popularity
    favourites
    trending
    genres
    tags { name rank isMediaSpoiler }
    source
    countryOfOrigin
    isAdult
    hashtag
    synonyms
    siteUrl
    trailer { id site thumbnail }
    studios { nodes { id name isAnimationStudio siteUrl } }
    nextAiringEpisode { episode airingAt timeUntilAiring }
    startDate { year month day }
    endDate { year month day }
    characters(sort: [ROLE, RELEVANCE], perPage: 25) {
        edges {
            role
            node { id name { full native } image { large } }
            voiceActors(language: JAPANESE) { id name { full native } image { large } languageV2 }
        }
    }
    staff(sort: RELEVANCE, perPage: 25) {
        edges {
            role
            node { id name { full native } image { large } }
        }
    }
    relations {
        edges {
            relationType(version: 2)
            node {
                id
                title { romaji english native }
                coverImage { large }
                format
                type
                status
                episodes
                meanScore
            }
        }
    }
    recommendations(sort: RATING_DESC, perPage: 10) {
        nodes {
            rating
            mediaRecommendation {
                id
                title { romaji english native }
                coverImage { large }
                format
                episodes
                status
                meanScore
                averageScore
            }
        }
    }
    externalLinks { url site type }
    streamingEpisodes { title thumbnail url site }
    stats {
        scoreDistribution { score amount }
        statusDistribution { status amount }
    }
`
)

var sortMap = map[string]string{
	"SCORE_DESC":      "SCORE_DESC",
	"POPULARITY_DESC": "POPULARITY_DESC",
	"TRENDING_DESC":   "TRENDING_DESC",
	"START_DATE_DESC": "START_DATE_DESC",
	"FAVOURITES_DESC": "FAVOURITES_DESC",
	"UPDATED_AT_DESC": "UPDATED_AT_DESC",
}

// req client global setup (TLS Impersonation for Cloudflare bypass)
var client = req.C().ImpersonateChrome().SetTimeout(15 * time.Second).SetCommonHeaders(map[string]string{
	"User-Agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/110.0.0.0 Safari/537.36",
	"Referer":            "https://www.miruro.tv/",
	"Origin":             "https://www.miruro.tv",
	"Accept":             "*/*",
	"Accept-Language":    "en-US,en;q=0.9",
	"sec-fetch-site":     "same-origin",
	"sec-fetch-mode":     "cors",
	"sec-fetch-dest":     "empty",
	"sec-ch-ua":          `"Chromium";v="110", "Not A(Brand";v="24", "Google Chrome";v="110"`,
	"sec-ch-ua-mobile":   "?0",
	"sec-ch-ua-platform": `"Windows"`,
})

func main() {
	_ = godotenv.Load()

	r := gin.Default()

	// CORS Middleware
	r.Use(cors.New(cors.Config{
		AllowOrigins: []string{
			"http://localhost:5173",
			"http://zenith.app",
		},
		AllowMethods:     []string{"*"},
		AllowHeaders:     []string{"*"},
		AllowCredentials: true,
	}))

	// Routes
	r.HEAD("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	r.GET("/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"version": CURRENT_VERSION})
	})
	r.GET("/", home)
	r.GET("/search", searchAnime)
	r.GET("/suggestions", searchSuggestions)
	r.GET("/filter", filterAnime)
	r.GET("/spotlight", getSpotlight)
	r.GET("/trending", getTrending)
	r.GET("/popular", getPopular)
	r.GET("/upcoming", getUpcoming)
	r.GET("/recent", getRecent)
	r.GET("/schedule", getSchedule)
	r.GET("/info/:anilist_id", getAnimeInfo)
	r.GET("/anime/:anilist_id/characters", getAnimeCharacters)
	r.GET("/anime/:anilist_id/relations", getAnimeRelations)
	r.GET("/anime/:anilist_id/recommendations", getAnimeRecommendations)
	r.GET("/episodes/:anilist_id", getEpisodesRoute)
	r.GET("/sources", getSourcesRoute)
	r.GET("/watch/:provider/:anilist_id/:category/:slug", getWatchSources)
	r.GET("/proxy/stream", proxyStream)

	log.Println("Server running on http://localhost:9189")
	if err := r.Run("localhost:9189"); err != nil {
		log.Fatal(err)
	}
}

// --- Helpers ---

func getQueryInt(c *gin.Context, key string, defaultVal int) int {
	val := c.Query(key)
	if val == "" {
		return defaultVal
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return parsed
}

func translateID(encodedID string) string {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encodedID, "="))
	if err == nil && strings.Contains(string(decoded), ":") {
		return string(decoded)
	}
	return encodedID
}

func deepTranslate(obj any) {
	switch v := obj.(type) {
	case map[string]any:
		for key, val := range v {
			if key == "id" {
				if strVal, ok := val.(string); ok {
					v[key] = translateID(strVal)
				}
			} else {
				deepTranslate(val)
			}
		}
	case []any:
		for _, item := range v {
			deepTranslate(item)
		}
	}
}

func decodePipeResponse(encodedStr string) (map[string]any, error) {
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encodedStr, "="))
	if err != nil {
		return nil, fmt.Errorf("base64 decode error: %w", err)
	}
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("gzip reader error: %w", err)
	}
	defer r.Close()
	decompressed, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gzip read error: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal(decompressed, &data); err != nil {
		return nil, fmt.Errorf("json unmarshal error: %w", err)
	}
	return data, nil
}

func encodePipeRequest(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}

func injectSourceSlugs(data map[string]any, anilistID int) map[string]any {
	providersRaw, ok := data["providers"]
	if !ok {
		return data
	}
	providers, ok := providersRaw.(map[string]any)
	if !ok {
		return data
	}

	for providerName, providerDataRaw := range providers {
		providerData, ok := providerDataRaw.(map[string]any)
		if !ok {
			continue
		}
		episodesRaw, ok := providerData["episodes"]
		if !ok {
			continue
		}

		var episodes map[string]any
		if epMap, ok := episodesRaw.(map[string]any); ok {
			episodes = epMap
		} else if epList, ok := episodesRaw.([]any); ok {
			episodes = map[string]any{"sub": epList}
			providerData["episodes"] = episodes
		} else {
			continue
		}

		for category, epListRaw := range episodes {
			epList, ok := epListRaw.([]any)
			if !ok {
				continue
			}
			for _, epRaw := range epList {
				ep, ok := epRaw.(map[string]any)
				if !ok {
					continue
				}
				idRaw, hasID := ep["id"]
				numRaw, hasNum := ep["number"]
				if hasID && hasNum {
					origID := fmt.Sprintf("%v", idRaw)
					num := fmt.Sprintf("%v", numRaw)
					prefix := origID
					if parts := strings.SplitN(origID, ":", 2); len(parts) > 0 {
						prefix = parts[0]
					}
					ep["id"] = fmt.Sprintf("watch/%s/%d/%s/%s-%s", providerName, anilistID, category, prefix, num)
				}
			}
		}
	}
	return data
}

func anilistQuery(query string, variables map[string]any) (map[string]any, error) {
	body := map[string]any{"query": query}
	if variables != nil {
		body["variables"] = variables
	}
	resp, err := req.C().R().SetBody(body).Post(AnilistUrl)
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("Anilist query failed with status %d", resp.GetStatusCode())
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	if err := resp.UnmarshalJson(&result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

func fetchRawEpisodes(anilistID int) (map[string]any, error) {
	payload := map[string]any{
		"path":    "episodes",
		"method":  "GET",
		"query":   map[string]any{"anilistId": anilistID},
		"body":    nil,
		"version": "0.1.0",
	}
	encodedReq := encodePipeRequest(payload)
	url := fmt.Sprintf("%s?e=%s", MiruroPipeUrl, encodedReq)
	resp, err := client.R().Get(url)
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("pipe response %d: %s", resp.GetStatusCode(), resp.String())
	}
	data, err := decodePipeResponse(strings.TrimSpace(resp.String()))
	if err != nil {
		return nil, err
	}
	deepTranslate(data)
	return data, nil
}

func fetchCollection(sortType string, status string, page int, perPage int) (map[string]any, error) {
	statusFilter := ""
	if status != "" {
		statusFilter = fmt.Sprintf(", status: %s", status)
	}
	gql := fmt.Sprintf(`
    query ($page: Int, $perPage: Int) {
        Page(page: $page, perPage: $perPage) {
            pageInfo { total currentPage lastPage hasNextPage perPage }
            media(type: ANIME, sort: [%s]%s, isAdult: false) {
                %s
            }
        }
    }
    `, sortType, statusFilter, MediaListFields)

	data, err := anilistQuery(gql, map[string]any{"page": page, "perPage": perPage})
	if err != nil {
		return nil, err
	}
	pageData, _ := data["Page"].(map[string]any)
	pageInfo, _ := pageData["pageInfo"].(map[string]any)

	return map[string]any{
		"page":        getMapInt(pageInfo, "currentPage", page),
		"perPage":     getMapInt(pageInfo, "perPage", perPage),
		"total":       getMapInt(pageInfo, "total", 0),
		"hasNextPage": getMapBool(pageInfo, "hasNextPage"),
		"results":     pageData["media"],
	}, nil
}

func getMapInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return def
}

func getMapBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// --- Handlers ---

func proxyStream(c *gin.Context) {
	targetURL := c.Query("url")
	if targetURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url parameter is required"})
		return
	}

	target, err := url.Parse(targetURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target URL"})
		return
	}

	customReferer := c.Query("referer")
	customOrigin := c.Query("origin")

	// Playlists are handled manually.
	// Everything else goes through ReverseProxy.
	if isM3U8URL(target) {
		proxyM3U8(c, target, customReferer, customOrigin)
		return
	}

	proxy := newStreamReverseProxy(target, customReferer, customOrigin)
	proxy.ServeHTTP(c.Writer, c.Request)
}

func isM3U8URL(u *url.URL) bool {
	if u == nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

func proxyM3U8(c *gin.Context, target *url.URL, customReferer, customOrigin string) {
	start := time.Now()

	reqBuilder := client.R()

	if customReferer != "" {
		reqBuilder.SetHeader("Referer", customReferer)
	}
	if customOrigin != "" {
		reqBuilder.SetHeader("Origin", customOrigin)
	} else {
		reqBuilder.SetHeader("Origin", "https://www.miruro.tv")
	}

	resp, err := reqBuilder.Get(target.String())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch M3U8: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if !resp.IsSuccessState() {
		c.JSON(resp.GetStatusCode(), gin.H{
			"error": fmt.Sprintf("upstream M3U8 returned %d", resp.GetStatusCode()),
		})
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read M3U8: " + err.Error()})
		return
	}

	manifest := string(body)

	// Only rewrite codecs for normal audio/video HLS.
	// Do not touch image-segment playlists like your .jpg manifests.
	if !isImageSegmentPlaylist(manifest) {
		manifest = strings.ReplaceAll(manifest, "mp4a.40.1", "mp4a.40.2")
		manifest = strings.ReplaceAll(manifest, "mp4a.40.34", "mp4a.40.2")
	} else {
		log.Printf("[M3U8] image playlist detected; codec rewrite disabled: %s (%v)", target.String(), time.Since(start))
	}

	rewritten := rewriteM3U8Manifest(manifest, target, customReferer, customOrigin)

	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.Header("Cache-Control", "no-cache")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Content-Length", strconv.Itoa(len(rewritten)))

	log.Printf("[M3U8] %s | %d bytes | %v", target.String(), len(rewritten), time.Since(start))

	c.Status(http.StatusOK)
	if _, err := c.Writer.Write([]byte(rewritten)); err != nil {
		log.Printf("[M3U8] write error: %v", err)
	}
}

func isImageSegmentPlaylist(manifest string) bool {
	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		u, err := url.Parse(trimmed)
		if err != nil {
			continue
		}

		p := strings.ToLower(u.Path)
		if strings.HasSuffix(p, ".jpg") ||
			strings.HasSuffix(p, ".jpeg") ||
			strings.HasSuffix(p, ".png") ||
			strings.HasSuffix(p, ".webp") {
			return true
		}
	}
	return false
}

func rewriteM3U8Manifest(manifest string, finalURL *url.URL, customReferer, customOrigin string) string {
	uriRegex := regexp.MustCompile(`URI=["']?([^"'\s>]+)["']?`)
	lines := strings.Split(manifest, "\n")
	out := make([]string, 0, len(lines))

	proxify := func(raw string) string {
		if raw == "" || strings.HasPrefix(raw, "data:") {
			return raw
		}

		absoluteURL := raw
		if parsed, err := url.Parse(raw); err == nil {
			absoluteURL = finalURL.ResolveReference(parsed).String()
		}

		proxied := "/proxy/stream?url=" + url.QueryEscape(absoluteURL)
		if customReferer != "" {
			proxied += "&referer=" + url.QueryEscape(customReferer)
		}
		if customOrigin != "" {
			proxied += "&origin=" + url.QueryEscape(customOrigin)
		}
		return proxied
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			out = append(out, line)
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, "URI=") {
				rewritten := uriRegex.ReplaceAllStringFunc(trimmed, func(match string) string {
					m := uriRegex.FindStringSubmatch(match)
					if len(m) < 2 {
						return match
					}
					return `URI="` + proxify(m[1]) + `"`
				})
				out = append(out, rewritten)
			} else {
				out = append(out, line)
			}
			continue
		}

		out = append(out, proxify(trimmed))
	}

	return strings.Join(out, "\n")
}

func newStreamReverseProxy(target *url.URL, customReferer, customOrigin string) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = target.Path
			req.URL.RawPath = target.RawPath
			req.URL.RawQuery = target.RawQuery
			req.Host = target.Host

			if customReferer != "" {
				req.Header.Set("Referer", customReferer)
			}
			if customOrigin != "" {
				req.Header.Set("Origin", customOrigin)
			} else {
				req.Header.Set("Origin", "https://www.miruro.tv")
			}

			req.Header.Set(
				"User-Agent",
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/110.0.0.0 Safari/537.36",
			)
		},
		ModifyResponse: func(resp *http.Response) error {
			log.Printf(
				"[SEGMENT] status=%d range=%q type=%s length=%s content-range=%s url=%s",
				resp.StatusCode,
				resp.Request.Header.Get("Range"),
				resp.Header.Get("Content-Type"),
				resp.Header.Get("Content-Length"),
				resp.Header.Get("Content-Range"),
				resp.Request.URL.String(),
			)

			resp.Header.Set("Access-Control-Allow-Origin", "*")
			resp.Header.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, Content-Type")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[SEGMENT] reverse proxy error: %v", err)
			http.Error(w, "stream proxy error", http.StatusBadGateway)
		},
	}

	return proxy
}

func home(c *gin.Context) {
	htmlContent := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Miruro API v3.0</title>
</head>
<body>
<h1>Miruro API v3.0 Live</h1>
</body>
</html>`
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(htmlContent))
}

func searchAnime(c *gin.Context) {
	query := c.Query("query")
	page := getQueryInt(c, "page", 1)
	perPage := getQueryInt(c, "per_page", 20)

	gql := fmt.Sprintf(`
    query ($search: String, $page: Int, $perPage: Int) {
        Page(page: $page, perPage: $perPage) {
            pageInfo { total currentPage lastPage hasNextPage perPage }
            media(search: $search, type: ANIME, sort: SEARCH_MATCH, isAdult: false) {
                %s
            }
        }
    }
    `, MediaListFields)

	data, err := anilistQuery(gql, map[string]any{"search": query, "page": page, "perPage": perPage})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	pageData, _ := data["Page"].(map[string]any)
	pageInfo, _ := pageData["pageInfo"].(map[string]any)

	c.JSON(http.StatusOK, gin.H{
		"page":        getMapInt(pageInfo, "currentPage", page),
		"perPage":     getMapInt(pageInfo, "perPage", perPage),
		"total":       getMapInt(pageInfo, "total", 0),
		"hasNextPage": getMapBool(pageInfo, "hasNextPage"),
		"results":     pageData["media"],
	})
}

func searchSuggestions(c *gin.Context) {
	query := c.Query("query")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter required"})
		return
	}
	gql := `
    query ($search: String) {
        Page(page: 1, perPage: 8) {
            media(search: $search, type: ANIME, sort: SEARCH_MATCH, isAdult: false) {
                id
                title { romaji english }
                coverImage { large }
                format
                status
                startDate { year }
                episodes
            }
        }
    }`

	data, err := anilistQuery(gql, map[string]any{"search": query})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	media, _ := data["Page"].(map[string]any)["media"].([]any)
	var results []map[string]any

	for _, itemRaw := range media {
		item := itemRaw.(map[string]any)
		titleMap := item["title"].(map[string]any)
		
		title := titleMap["english"]
		if title == nil {
			title = titleMap["romaji"]
		}

		year := 0
		if sd, ok := item["startDate"].(map[string]any); ok && sd["year"] != nil {
			year = int(sd["year"].(float64))
		}

		res := map[string]any{
			"id":           item["id"],
			"title":        title,
			"title_romaji": titleMap["romaji"],
			"poster":       item["coverImage"].(map[string]any)["large"],
			"format":       item["format"],
			"status":       item["status"],
			"year":         year,
			"episodes":     item["episodes"],
		}
		results = append(results, res)
	}
	c.JSON(http.StatusOK, gin.H{"suggestions": results})
}

func filterAnime(c *gin.Context) {
	genre := c.Query("genre")
	tag := c.Query("tag")
	year := c.Query("year")
	season := strings.ToUpper(c.Query("season"))
	format := strings.ToUpper(c.Query("format"))
	status := strings.ToUpper(c.Query("status"))
	sort := c.Query("sort")
	if sort == "" {
		sort = "POPULARITY_DESC"
	}
	
	page := getQueryInt(c, "page", 1)
	perPage := getQueryInt(c, "per_page", 20)

	args := []string{"type: ANIME", fmt.Sprintf("sort: [%s]", sortMap[sort])}
	variables := map[string]any{"page": page, "perPage": perPage}
	varTypes := []string{"$page: Int", "$perPage: Int"}

	if genre != "" {
		args = append(args, "genre: $genre")
		variables["genre"] = genre
		varTypes = append(varTypes, "$genre: String")
	}
	if tag != "" {
		args = append(args, "tag: $tag")
		variables["tag"] = tag
		varTypes = append(varTypes, "$tag: String")
	}
	if year != "" {
		args = append(args, "seasonYear: $seasonYear")
		yInt, _ := strconv.Atoi(year)
		variables["seasonYear"] = yInt
		varTypes = append(varTypes, "$seasonYear: Int")
	}
	if season != "" {
		args = append(args, "season: $season")
		variables["season"] = season
		varTypes = append(varTypes, "$season: MediaSeason")
	}
	if format != "" {
		args = append(args, "format: $format")
		variables["format"] = format
		varTypes = append(varTypes, "$format: MediaFormat")
	}
	if status != "" {
		args = append(args, "status: $status")
		variables["status"] = status
		varTypes = append(varTypes, "$status: MediaStatus")
	}

	gql := fmt.Sprintf(`
    query (%s) {
        Page(page: $page, perPage: $perPage) {
            pageInfo { total currentPage lastPage hasNextPage perPage }
            media(%s, isAdult: false) {
                %s
            }
        }
    }`, strings.Join(varTypes, ", "), strings.Join(args, ", "), MediaListFields)

	data, err := anilistQuery(gql, variables)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	pageData, _ := data["Page"].(map[string]any)
	pageInfo, _ := pageData["pageInfo"].(map[string]any)

	c.JSON(http.StatusOK, gin.H{
		"page":        getMapInt(pageInfo, "currentPage", page),
		"perPage":     getMapInt(pageInfo, "perPage", perPage),
		"total":       getMapInt(pageInfo, "total", 0),
		"hasNextPage": getMapBool(pageInfo, "hasNextPage"),
		"results":     pageData["media"],
	})
}

func getSpotlight(c *gin.Context) {
	gql := fmt.Sprintf(`
    query {
        Page(page: 1, perPage: 10) {
            media(sort: [TRENDING_DESC, POPULARITY_DESC], type: ANIME, isAdult: false) {
                %s
            }
        }
    }`, MediaListFields)

	data, err := anilistQuery(gql, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	media := data["Page"].(map[string]any)["media"]
	c.JSON(http.StatusOK, gin.H{"results": media})
}

func getTrending(c *gin.Context) {
	data, err := fetchCollection("TRENDING_DESC", "", getQueryInt(c, "page", 1), getQueryInt(c, "per_page", 20))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func getPopular(c *gin.Context) {
	data, err := fetchCollection("POPULARITY_DESC", "", getQueryInt(c, "page", 1), getQueryInt(c, "per_page", 20))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func getUpcoming(c *gin.Context) {
	data, err := fetchCollection("POPULARITY_DESC", "NOT_YET_RELEASED", getQueryInt(c, "page", 1), getQueryInt(c, "per_page", 20))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func getRecent(c *gin.Context) {
	data, err := fetchCollection("START_DATE_DESC", "RELEASING", getQueryInt(c, "page", 1), getQueryInt(c, "per_page", 20))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func getSchedule(c *gin.Context) {
	page := getQueryInt(c, "page", 1)
	perPage := getQueryInt(c, "per_page", 20)

	gql := fmt.Sprintf(`
    query ($page: Int, $perPage: Int) {
        Page(page: $page, perPage: $perPage) {
            pageInfo { total currentPage lastPage hasNextPage perPage }
            airingSchedules(notYetAired: true, sort: TIME) {
                episode
                airingAt
                timeUntilAiring
                media {
                    %s
                }
            }
        }
    }`, MediaListFields)

	data, err := anilistQuery(gql, map[string]any{"page": page, "perPage": perPage})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	pageData := data["Page"].(map[string]any)
	pageInfo := pageData["pageInfo"].(map[string]any)
	schedules := pageData["airingSchedules"].([]any)
	var results []map[string]any

	for _, sRaw := range schedules {
		item := sRaw.(map[string]any)
		entry := item["media"].(map[string]any)
		entry["next_episode"] = item["episode"]
		entry["airingAt"] = item["airingAt"]
		entry["timeUntilAiring"] = item["timeUntilAiring"]
		results = append(results, entry)
	}

	c.JSON(http.StatusOK, gin.H{
		"page":        getMapInt(pageInfo, "currentPage", page),
		"perPage":     getMapInt(pageInfo, "perPage", perPage),
		"total":       getMapInt(pageInfo, "total", 0),
		"hasNextPage": getMapBool(pageInfo, "hasNextPage"),
		"results":     results,
	})
}

func getAnimeInfo(c *gin.Context) {
	anilistID, err := strconv.Atoi(c.Param("anilist_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID format"})
		return
	}

	gql := fmt.Sprintf(`
    query ($id: Int) {
        Media(id: $id, type: ANIME) {
            %s
        }
    }`, MediaFullFields)

	data, err := anilistQuery(gql, map[string]any{"id": anilistID})
	if err != nil || data["Media"] == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Anime not found"})
		return
	}
	c.JSON(http.StatusOK, data["Media"])
}

func getAnimeCharacters(c *gin.Context) {
	anilistID, _ := strconv.Atoi(c.Param("anilist_id"))
	page := getQueryInt(c, "page", 1)
	perPage := getQueryInt(c, "per_page", 25)

	gql := `
    query ($id: Int, $page: Int, $perPage: Int) {
        Media(id: $id, type: ANIME) {
            id
            title { romaji english }
            characters(sort: [ROLE, RELEVANCE], page: $page, perPage: $perPage) {
                pageInfo { total currentPage lastPage hasNextPage perPage }
                edges {
                    role
                    node {
                        id
                        name { full native userPreferred }
                        image { large medium }
                        description
                        gender
                        dateOfBirth { year month day }
                        age
                        favourites
                        siteUrl
                    }
                    voiceActors {
                        id
                        name { full native }
                        image { large }
                        languageV2
                    }
                }
            }
        }
    }`

	data, err := anilistQuery(gql, map[string]any{"id": anilistID, "page": page, "perPage": perPage})
	if err != nil || data["Media"] == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Anime not found"})
		return
	}

	media := data["Media"].(map[string]any)
	chars := media["characters"].(map[string]any)
	pageInfo := chars["pageInfo"].(map[string]any)

	c.JSON(http.StatusOK, gin.H{
		"page":        getMapInt(pageInfo, "currentPage", page),
		"perPage":     getMapInt(pageInfo, "perPage", perPage),
		"total":       getMapInt(pageInfo, "total", 0),
		"hasNextPage": getMapBool(pageInfo, "hasNextPage"),
		"characters":  chars["edges"],
	})
}

func getAnimeRelations(c *gin.Context) {
	anilistID, _ := strconv.Atoi(c.Param("anilist_id"))

	gql := `
    query ($id: Int) {
        Media(id: $id, type: ANIME) {
            id
            title { romaji english }
            relations {
                edges {
                    relationType(version: 2)
                    node {
                        id
                        title { romaji english native }
                        coverImage { large }
                        bannerImage
                        format
                        type
                        status
                        episodes
                        chapters
                        meanScore
                        averageScore
                        popularity
                        startDate { year month day }
                    }
                }
            }
        }
    }`

	data, err := anilistQuery(gql, map[string]any{"id": anilistID})
	if err != nil || data["Media"] == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Anime not found"})
		return
	}

	media := data["Media"].(map[string]any)
	c.JSON(http.StatusOK, gin.H{
		"id":        media["id"],
		"title":     media["title"],
		"relations": media["relations"].(map[string]any)["edges"],
	})
}

func getAnimeRecommendations(c *gin.Context) {
	anilistID, _ := strconv.Atoi(c.Param("anilist_id"))
	page := getQueryInt(c, "page", 1)
	perPage := getQueryInt(c, "per_page", 10)

	gql := `
    query ($id: Int, $page: Int, $perPage: Int) {
        Media(id: $id, type: ANIME) {
            id
            title { romaji english }
            recommendations(sort: RATING_DESC, page: $page, perPage: $perPage) {
                pageInfo { total currentPage lastPage hasNextPage perPage }
                nodes {
                    rating
                    mediaRecommendation {
                        id
                        title { romaji english native }
                        coverImage { large extraLarge }
                        bannerImage
                        format
                        episodes
                        status
                        meanScore
                        averageScore
                        popularity
                        genres
                        startDate { year }
                    }
                }
            }
        }
    }`

	data, err := anilistQuery(gql, map[string]any{"id": anilistID, "page": page, "perPage": perPage})
	if err != nil || data["Media"] == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Anime not found"})
		return
	}

	media := data["Media"].(map[string]any)
	recs := media["recommendations"].(map[string]any)
	pageInfo := recs["pageInfo"].(map[string]any)

	c.JSON(http.StatusOK, gin.H{
		"page":            getMapInt(pageInfo, "currentPage", page),
		"perPage":         getMapInt(pageInfo, "perPage", perPage),
		"total":           getMapInt(pageInfo, "total", 0),
		"hasNextPage":     getMapBool(pageInfo, "hasNextPage"),
		"recommendations": recs["nodes"],
	})
}

func getEpisodesRoute(c *gin.Context) {
	anilistID, err := strconv.Atoi(c.Param("anilist_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID format"})
		return
	}
	data, err := fetchRawEpisodes(anilistID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, injectSourceSlugs(data, anilistID))
}

func getSourcesRoute(c *gin.Context) {
	episodeID := c.Query("episodeId")
	provider := c.Query("provider")
	anilistID, _ := strconv.Atoi(c.Query("anilistId"))
	category := c.Query("category")
	if category == "" {
		category = "sub"
	}

	encID := strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(episodeID)), "=")
	payload := map[string]any{
		"path":   "sources",
		"method": "GET",
		"query": map[string]any{
			"episodeId": encID,
			"provider":  provider,
			"category":  category,
			"anilistId": anilistID,
		},
		"body":    nil,
		"version": "0.1.0",
	}

	encodedReq := encodePipeRequest(payload)
	urlStr := fmt.Sprintf("%s?e=%s", MiruroPipeUrl, encodedReq)
	
	resp, err := client.R().Get(urlStr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !resp.IsSuccessState() {
		c.JSON(resp.GetStatusCode(), gin.H{"error": "pipe request failed", "body": resp.String()})
		return
	}

	data, err := decodePipeResponse(strings.TrimSpace(resp.String()))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func getWatchSources(c *gin.Context) {
	provider := c.Param("provider")
	anilistID, _ := strconv.Atoi(c.Param("anilist_id"))
	category := c.Param("category")
	slug := c.Param("slug")

	data, err := fetchRawEpisodes(anilistID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	provData, ok := data["providers"].(map[string]any)[provider].(map[string]any)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Provider not found"})
		return
	}

	epList, ok := provData["episodes"].(map[string]any)[category].([]any)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Category not found"})
		return
	}

	var targetID string
	for _, epRaw := range epList {
		ep := epRaw.(map[string]any)
		origID := fmt.Sprintf("%v", ep["id"])
		prefix := origID
		if parts := strings.SplitN(origID, ":", 2); len(parts) > 0 {
			prefix = parts[0]
		}
		generated := fmt.Sprintf("%s-%v", prefix, ep["number"])
		if generated == slug {
			targetID = origID
			break
		}
	}

	if targetID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("Episode slug '%s' not found for provider %s", slug, provider)})
		return
	}

	c.Request.URL.RawQuery = fmt.Sprintf("episodeId=%s&provider=%s&anilistId=%d&category=%s", targetID, provider, anilistID, category)
	getSourcesRoute(c)
}
