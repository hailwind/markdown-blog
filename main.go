package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gaowei-space/markdown-blog/internal/app"
	"github.com/gaowei-space/markdown-blog/internal/utils"
	"github.com/urfave/cli/v2"
	"github.com/urfave/cli/v2/altsrc"
)

//go:generate go-bindata -fs -o internal/bindata/views/views.go -pkg=views -prefix=web/views ./web/views/...
//go:generate go-bindata -fs -o internal/bindata/assets/assets.go -pkg=assets -prefix=web/assets ./web/assets/...

var (
	MdDir                = "md/"
	IdxDb                = "idx.db"
	Title                = "Blog"
	AppVersion           = "1.1.1"
	BuildDate, GitCommit string
)

// web服务器默认端口
const DefaultPort = 5006

func main() {
	cliApp := cli.NewApp()
	cliApp.Name = "markdown-blog"
	cliApp.Usage = "Markdown Blog App"
	cliApp.Version, _ = utils.FormatAppVersion(AppVersion, GitCommit, BuildDate)
	cliApp.Commands = getCommands()
	cliApp.Flags = append(cliApp.Flags, []cli.Flag{}...)
	ctx, cancel := context.WithCancel(context.Background())

	if isHelp() {
		// 仅打印帮助信息，无需后台
		cliApp.RunContext(ctx, os.Args)
	} else {
		go cliApp.RunContext(ctx, os.Args)
		// 优雅关机
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Println("Shutdown Markdown-Blog Server ...")
		cancel()
		ticker := time.NewTicker(time.Second)
		<-ticker.C
	}
}

func isHelp() bool {
	for _, arg := range os.Args {
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return true
		}
	}
	return false
}

func getCommands() []*cli.Command {
	flags := flags()
	web := webCommand(flags)

	return []*cli.Command{web}
}

func webCommand(flags []cli.Flag) *cli.Command {
	web := cli.Command{
		Name:   "web",
		Usage:  "Run blog web server",
		Action: app.RunWeb,
		Flags:  flags,
		Before: altsrc.InitInputSourceWithContext(flags, altsrc.NewYamlSourceFromFlagFunc("config")),
	}
	return &web
}

func flags() []cli.Flag {
	commonFlags := []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Value: "",
			Usage: "Load configuration from `FILE`, default is empty",
		},
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "dir",
			Aliases: []string{"d"},
			Value:   MdDir,
			Usage:   "Markdown files dir",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "idxdb",
			Aliases: []string{"f"},
			Value:   IdxDb,
			Usage:   "Fulltext Index Database File",
		}),
		altsrc.NewBoolFlag(&cli.BoolFlag{
			Name:  "forceidx",
			Value: false,
			Usage: "Forece to ReIndex documents",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "title",
			Aliases: []string{"t"},
			Value:   Title,
			Usage:   "Blog title",
		}),
		altsrc.NewIntFlag(&cli.IntFlag{
			Name:    "port",
			Aliases: []string{"p"},
			Value:   DefaultPort,
			Usage:   "Bind port",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "env",
			Aliases: []string{"e"},
			Value:   "prod",
			Usage:   "Runtime environment, dev|test|prod",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "index",
			Aliases: []string{"i"},
			Value:   "",
			Usage:   "Home page, default is empty",
		}),
		altsrc.NewIntFlag(&cli.IntFlag{
			Name:    "cache",
			Aliases: []string{"c"},
			Value:   3,
			Usage:   "The cache time unit is minutes, this parameter takes effect in the prod environment",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "icp",
			Value: "",
			Usage: "ICP, default is empty",
		}), altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "isf",
			Value: "",
			Usage: "National Internet Security Filing, default is empty",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "copyright",
			Value: strconv.Itoa(time.Now().Year()),
			Usage: "Copyright, default the current year, such as 2024",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "fdir",
			Value: "public",
			Usage: "File directory name",
		}),
	}

	gitalkFlags := []cli.Flag{
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "gitalk.client-id",
			Usage: "Set up Gitalk ClientId, default is empty",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "gitalk.client-secret",
			Usage: "Set up Gitalk ClientSecret, default is empty",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "gitalk.repo",
			Usage: "Set up Gitalk Repo, default is empty",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:  "gitalk.owner",
			Usage: "Set up Gitalk Repo, default is empty",
		}),
		altsrc.NewStringSliceFlag(&cli.StringSliceFlag{
			Name:  "gitalk.admin",
			Usage: "Set up Gitalk Admin, default is `[gitalk.owner]`",
		}),
		altsrc.NewStringSliceFlag(&cli.StringSliceFlag{
			Name:  "gitalk.labels",
			Usage: "Set up Gitalk Admin, default is `[\"gitalk\"]`",
		}),
	}

	flags := append(commonFlags, gitalkFlags...)

	analyzerFlags := []cli.Flag{
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "analyzer-baidu",
			Aliases: []string{"ab"},
			Value:   "",
			Usage:   "Set up Baidu Analyzer, default is empty",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "analyzer-google",
			Aliases: []string{"ag"},
			Value:   "",
			Usage:   "Set up Google Analyzer, default is empty",
		}),
	}

	flags = append(flags, analyzerFlags...)

	ignoreFlags := []cli.Flag{
		altsrc.NewStringSliceFlag(&cli.StringSliceFlag{
			Name:  "ignore-file",
			Usage: "Set up ignore file, eg: demo.md",
		}),
		altsrc.NewStringSliceFlag(&cli.StringSliceFlag{
			Name:  "ignore-path",
			Usage: "Set up ignore path, eg: demo",
		}),
	}

	flags = append(flags, ignoreFlags...)
	return flags
}
