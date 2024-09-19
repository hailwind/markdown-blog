package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/structs"
	"github.com/gaowei-space/markdown-blog/internal/api"
	"github.com/gaowei-space/markdown-blog/internal/bindata/assets"
	"github.com/gaowei-space/markdown-blog/internal/bindata/views"
	"github.com/gaowei-space/markdown-blog/internal/types"
	"github.com/gaowei-space/markdown-blog/internal/utils"
	"github.com/kataras/iris/v12"
	"github.com/kataras/iris/v12/middleware/accesslog"
	"github.com/kataras/iris/v12/view"
	"github.com/microcosm-cc/bluemonday"
	"github.com/russross/blackfriday/v2"
	"github.com/urfave/cli/v2"
)

var (
	MdDir      string
	Env        string
	Title      string
	Index      string
	ICP        string
	ISF        string
	FDir       string
	Copyright  int64
	LayoutFile = "layouts/layout.html"
	LogsDir    = "cache/logs/"
	TocPrefix  = "[toc]"
	IgnoreFile = []string{`favicon.ico`, `.DS_Store`, `.gitignore`, `README.md`}
	IgnorePath = []string{`.git`, `assets`}
	Cache      time.Duration
	Analyzer   types.Analyzer
	Gitalk     types.Gitalk
)

// web服务器默认端口
const DefaultPort = 5006

func RunWeb(ctx *cli.Context) error {
	go RunIndex(ctx)
	initParams(ctx)

	app := iris.New()

	setLog(app)

	app.RegisterView(getTmpl())
	app.OnErrorCode(iris.StatusNotFound, api.NotFound)
	app.OnErrorCode(iris.StatusInternalServerError, api.InternalServerError)

	setIndexAuto := false
	if Index == "" {
		setIndexAuto = true
	}

	app.Use(func(ctx iris.Context) {
		activeNav := getActiveNav(ctx)

		navs, firstNav := getNavs(activeNav)

		firstLink := utils.CustomURLEncode(strings.TrimPrefix(firstNav.Link, "/"))
		if setIndexAuto && Index != firstLink {
			Index = firstLink
		}

		// 设置 Gitalk ID
		Gitalk.Id = utils.MD5(activeNav)

		ctx.ViewData("Gitalk", Gitalk)
		ctx.ViewData("Analyzer", Analyzer)
		ctx.ViewData("Title", Title)
		ctx.ViewData("Nav", navs)
		ctx.ViewData("ICP", ICP)
		ctx.ViewData("ISF", ISF)
		ctx.ViewData("Copyright", Copyright)
		ctx.ViewData("ActiveNav", activeNav)
		ctx.ViewLayout(LayoutFile)

		ctx.Next()
	})

	app.Favicon("./favicon.ico")
	app.HandleDir("/static", getStatic())
	app.Get("/search", searchHandler)
	app.Get("/{f:path}", iris.Cache(Cache), articleHandler)
	app.Get(fmt.Sprintf("/%s/{f:path}", FDir), serveFileHandler)

	app.Run(iris.Addr(":" + strconv.Itoa(parsePort(ctx))))

	return nil
}

func getStatic() interface{} {
	if Env == "prod" {
		return assets.AssetFile()
	} else {
		return "./web/assets"
	}
}

func getTmpl() *view.HTMLEngine {
	if Env == "prod" {
		return iris.HTML(views.AssetFile(), ".html").Reload(true)
	} else {
		return iris.HTML("./web/views", ".html").Reload(true)
	}
}

func initParams(ctx *cli.Context) {
	MdDir = ctx.String("dir")
	if strings.TrimSpace(MdDir) == "" {
		log.Panic("Markdown files folder cannot be empty")
	}
	MdDir, _ = filepath.Abs(MdDir)

	Env = ctx.String("env")
	Title = ctx.String("title")
	Index = ctx.String("index")
	ICP = ctx.String("icp")
	ISF = ctx.String("isf")
	Copyright = ctx.Int64("copyright")
	FDir = ctx.String("fdir")

	Cache = time.Minute * 0
	if Env == "prod" {
		Cache = time.Minute * time.Duration(ctx.Int64("cache"))
	}

	// 设置分析器
	Analyzer.SetAnalyzer(ctx.String("analyzer-baidu"), ctx.String("analyzer-google"))

	// 设置Gitalk
	Gitalk.SetGitalk(ctx.String("gitalk.client-id"), ctx.String("gitalk.client-secret"), ctx.String("gitalk.repo"), ctx.String("gitalk.owner"), ctx.StringSlice("gitalk.admin"), ctx.StringSlice("gitalk.labels"))

	// 忽略文件
	IgnoreFile = append(IgnoreFile, ctx.StringSlice("ignore-file")...)
	IgnorePath = append(IgnorePath, FDir)
	IgnorePath = append(IgnorePath, ctx.StringSlice("ignore-path")...)
}

func setLog(app *iris.Application) {
	os.MkdirAll(LogsDir, 0777)
	f, _ := os.OpenFile(LogsDir+"access-"+time.Now().Format("20060102")+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)

	if Env == "prod" {
		app.Logger().SetOutput(f)
	} else {
		app.Logger().SetLevel("debug")
		app.Logger().Debugf(`Log level set to "debug"`)
	}

	// Close the file on shutdown.
	app.ConfigureHost(func(su *iris.Supervisor) {
		su.RegisterOnShutdown(func() {
			f.Close()
		})
	})

	ac := accesslog.New(f)
	ac.AddOutput(app.Logger().Printer)
	app.UseRouter(ac.Handler)
	app.Logger().Debugf("Using <%s> to log requests", f.Name())
}

func parsePort(ctx *cli.Context) int {
	port := DefaultPort
	if ctx.IsSet("port") {
		port = ctx.Int("port")
	}
	if port <= 0 || port >= 65535 {
		port = DefaultPort
	}

	return port
}

func getNavs(activeNav string) ([]map[string]interface{}, utils.Node) {
	var option utils.Option
	option.RootPath = []string{MdDir}
	option.SubFlag = true
	option.IgnorePath = IgnorePath
	option.IgnoreFile = IgnoreFile
	tree, _ := utils.Explorer(option)

	navs := make([]map[string]interface{}, 0)
	for _, v := range tree.Children {
		for _, item := range v.Children {
			searchActiveNav(item, activeNav)
			navs = append(navs, structs.Map(item))
		}
	}

	firstNav := getFirstNav(*tree.Children[0])

	return navs, firstNav
}

func searchActiveNav(node *utils.Node, activeNav string) {
	link_str, _ := url.QueryUnescape(node.Link)
	if !node.IsDir && strings.TrimPrefix(link_str, "/") == activeNav {
		node.Active = "active"
		return
	}
	if len(node.Children) > 0 {
		for _, v := range node.Children {
			searchActiveNav(v, activeNav)
		}
	}
}

func getFirstNav(node utils.Node) utils.Node {
	if !node.IsDir {
		return node
	}
	return getFirstNav(*node.Children[0])
}

func getActiveNav(ctx iris.Context) string {
	f := ctx.Params().Get("f")
	if f == "" {
		f = Index
	}
	return f
}

func serveFileHandler(ctx iris.Context) {
	f := ctx.Params().Get("f")
	file := MdDir + "/" + FDir + "/" + f
	ctx.ServeFile(file)
}

func serveAssetsFileHandler(ctx iris.Context) {
	f := ctx.Params().Get("f")
	file := MdDir + "/" + f
	ctx.ServeFile(file)
}

func articleHandler(ctx iris.Context) {
	f := getActiveNav(ctx)
	regx := regexp.MustCompile(`.*\.(jpeg|jpg|pjpg|gif|png|webp|svg)$`)
	if regx.MatchString(f) {
		// log.Printf("serveAssetsFileHandler - %s", f)
		serveAssetsFileHandler(ctx)
		return
	}

	if utils.IsInSlice(IgnoreFile, f) {
		return
	}

	mdfile := MdDir + "/" + f + ".md"

	_, err := os.Stat(mdfile)
	if err != nil {
		ctx.StatusCode(404)
		ctx.Application().Logger().Errorf("Not Found '%s', Path is %s", mdfile, ctx.Path())
		return
	}

	bytes, err := os.ReadFile(mdfile)
	if err != nil {
		ctx.StatusCode(500)
		ctx.Application().Logger().Errorf("ReadFile Error '%s', Path is %s", mdfile, ctx.Path())
		return
	}
	tmp := strings.Split(f, "/")
	title := tmp[len(tmp)-1]
	ctx.ViewData("Title", Title)
	ctx.ViewData("ArticleTitle", title)
	ctx.ViewData("Article", mdToHtml(bytes))

	ctx.View("index.html")
}

func mdToHtml(content []byte) template.HTML {
	strs := string(content)

	var htmlFlags blackfriday.HTMLFlags

	if strings.HasPrefix(strs, TocPrefix) {
		htmlFlags |= blackfriday.TOC
		strs = strings.Replace(strs, TocPrefix, "<br/><br/>", 1)
	}

	renderer := blackfriday.NewHTMLRenderer(blackfriday.HTMLRendererParameters{
		Flags: htmlFlags,
	})

	// fix windows \r\n
	unix := strings.ReplaceAll(strs, "\r\n", "\n")

	unsafe := blackfriday.Run([]byte(unix), blackfriday.WithRenderer(renderer), blackfriday.WithExtensions(blackfriday.CommonExtensions))

	// 创建bluemonday策略，只允许<span>标签及其style属性
	p := bluemonday.UGCPolicy()
	p.AllowElements("span")                  // 只允许<span>标签
	p.AllowAttrs("style").OnElements("span") // 在<span>上允许使用style属性

	// 使用自定义的bluemonday策略来清理HTML
	html := p.SanitizeBytes(unsafe)

	return template.HTML(string(html))
}

func SubStr(str string, length int) string {
	if length < 1 {
		return ""
	}
	var runes []rune
	for i, r := range str {
		if i+1 > length {
			break
		}
		runes = append(runes, r)
	}
	return string(runes)
}

type Search struct {
	Query string `json:"query"`
	Page  int    `json:"page"`
	Limit int    `json:"limit"`
	Order string `json:"order"`
}

type SMetadata struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Md5sum string `json:"md5sum"`
}

type SDocument struct {
	Id       int64     `json:"id"`
	Text     string    `json:"text"`
	Document SMetadata `json:"document"`
	Score    int       `json:"score"`
}

func (d *SDocument) Summary() string {
	if len(d.Text) >= 380 {
		return SubStr(d.Text, 380)
	} else {
		return d.Text
	}
}

type SData struct {
	Time      float32     `json:"time"`
	Total     int         `json:"total"`
	PageCount int         `json:"pageCount"`
	Page      int         `json:"page"`
	Limit     int         `json:"limit"`
	Words     []string    `json:"words"`
	Documents []SDocument `json:"documents"`
}

type SMessage struct {
	State   bool   `json:"state"`
	Message string `json:"message"`
	Data    SData  `json:"data"`
}

func searchHandler(ctx iris.Context) {
	ctx.ViewData("Title", Title)
	query := ctx.URLParam("keyword")
	page := 1
	pageStr := ctx.URLParam("page")
	if pageStr != "" {
		page, _ = strconv.Atoi(pageStr)
	}
	limit := 10
	limitStr := ctx.URLParam("limit")
	if limitStr != "" {
		limit, _ = strconv.Atoi(limitStr)
	}
	search := Search{
		Query: query,
		Page:  page,
		Limit: limit,
		Order: "desc",
	}
	if data, err := json.Marshal(search); err == nil {
		body := bytes.NewBuffer(data)
		if resp, err := http.Post(gofoundQuery, "application/json", body); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				dec := json.NewDecoder(resp.Body)
				msg := SMessage{}
				if err := dec.Decode(&msg); err == nil {
					if msg.Message == "success" {
						ctx.ViewData("Data", msg.Data)
						ctx.ViewData("Keyword", query)
						if msg.Data.Page > 1 {
							ctx.ViewData("Prev", msg.Data.Page-1)
						}
						if msg.Data.PageCount > msg.Data.Page {
							ctx.ViewData("Next", msg.Data.Page+1)
						}
						log.Printf("Total: %d", msg.Data.Total)
					}
				}
			}
		} else {
			log.Printf("post err: %s", err)
		}
	} else {
		log.Printf("error: %s", err)
	}
	ctx.View("search.html")
}
