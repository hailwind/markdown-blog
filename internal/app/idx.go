package app

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/radovskyb/watcher"
	"github.com/urfave/cli/v2"
)

var (
	DEBUG         = false
	dryIndex      = false
	gofoundIndex  = "http://127.0.0.1:5678/api/index?database=default"
	gofoundRemove = "http://127.0.0.1:5678/api/index/remove?database=default"
	gofoundQuery  = "http://127.0.0.1:5678/api/query?database=default"
	gofoundDrop   = "http://127.0.0.1:5678/api/db/drop?database=default"
)

func exists(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	} else {
		return true
	}
}

func md5sum(path string) string {
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		h := md5.New()
		if _, err := io.Copy(h, f); err == nil {
			return fmt.Sprintf("%x", h.Sum(nil))
		}
	}
	return ""
}

func RunIndex(ctx *cli.Context) {
	log.Printf("[INDEXSERVER] RUNNING INDEX SERVER....")
	mdDir := ctx.String("dir")
	idxdb := ctx.String("idxdb")
	forceidx := ctx.Bool("forceidx")

	i := NewIndexer(mdDir, idxdb, forceidx, ctx.Context)
	i.FirstRun()
	go i.Watch()
	i.Run()
}

func NewIndexer(mdDir, idxdb string, force bool, ctx context.Context) *Indexer {
	i := Indexer{
		MdDir: mdDir,
		Force: force,
		ctx:   ctx,
	}
	i.InitDB(idxdb)
	i.InitWatcher()
	return &i
}

type Indexer struct {
	db    *sql.DB
	w     *watcher.Watcher
	MdDir string
	Force bool
	ctx   context.Context
}

func (i *Indexer) InitDB(idxdb string) {
	if !exists(i.MdDir) {
		log.Fatalf("[INDEXSERVER] Markdown Dir %s is not exist, quit.", i.MdDir)
		return
	}

	if db, err := sql.Open("sqlite", idxdb); err != nil {
		log.Fatal("[INDEXSERVER] ", err)
	} else {
		i.db = db
	}

	var ver string
	row := i.db.QueryRow("select sqlite_version() as ver")
	row.Scan(&ver)
	log.Printf("[INDEXSERVER] sqlite db path: %#v version: %#v", idxdb, ver)
	if _, err := i.db.Exec("CREATE TABLE IF NOT EXISTS articles (id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT, md5sum TEXT, modtime DATETIME DEFAULT CURRENT_TIMESTAMP)"); err != nil {
		log.Fatal("[INDEXSERVER] CANNOT CREATE TABLE.")
	}
}

func (i *Indexer) InitWatcher() {
	w := watcher.New()
	// SetMaxEvents to 1 to allow at most 1 event's to be received
	// on the Event channel per watching cycle.
	//
	// If SetMaxEvents is not set, the default is to send all events.
	w.SetMaxEvents(1)
	// Only notify rename and move events.
	w.FilterOps(watcher.Create, watcher.Remove, watcher.Write, watcher.Move, watcher.Rename)
	// Only files that match the regular expression during file listings
	// will be watched.
	r := regexp.MustCompile(`\.md$`)
	w.AddFilterHook(watcher.RegexFilterHook(r, false))
	log.Printf("[INDEXSERVER] Watching %s", i.MdDir)
	// Watch this folder for changes.
	// if err := w.Add(i.MdDir); err != nil {
	// 	log.Println(err)
	// }
	// Watch this folder recursively for changes.
	if err := w.AddRecursive(i.MdDir); err != nil {
		log.Println(err)
	}

	// Trigger 2 events after watcher started.
	// go func() {
	// 	w.Wait()
	// 	w.TriggerEvent(watcher.Create, nil)
	// 	w.TriggerEvent(watcher.Remove, nil)
	// 	w.TriggerEvent(watcher.Write, nil)
	// }()
	i.w = w
}

// 监听/遍历目录中的文件
func (i *Indexer) Watch() {
	// Start the watching process - it'll check for changes every 1s.
	var d time.Duration
	if Env == "prod" {
		d = time.Minute
	} else {
		d = time.Second
	}
	if err := i.w.Start(d); err != nil {
		log.Println(err)
	}
}

func (i *Indexer) Find(path string) (*Document, bool) {
	var doc Document
	if rows, err := i.db.Query("SELECT id, path, md5sum, modtime FROM articles WHERE path=?", path); err == nil {
		defer rows.Close()
		if rows.Next() {
			if err1 := rows.Scan(&doc.Id, &doc.Path, &doc.Md5sum, &doc.ModTime); err1 == nil {
				return &doc, true
			}
		} else {
			return nil, true
		}
	} else {
		log.Printf("[INDEXSERVER] QUERY path %s ERROR: %s", path, err)
	}
	return nil, false
}

func (i *Indexer) Insert(doc *Document) (int64, bool) {
	if r, err := i.db.Exec("INSERT INTO articles (path,md5sum,modtime) VALUES (?,?,?)", doc.Path, doc.Md5sum, doc.ModTime); err == nil {
		if id, err3 := r.LastInsertId(); err3 == nil {
			doc.Id = id
			return id, true
		}
	} else {
		log.Printf("[INDEXSERVER] INSERT DOC %s ERROR: %s", doc, err)
	}
	return -1, false
}

func (i *Indexer) Update(doc *Document) (int64, bool) {
	if r, err := i.db.Exec("UPDATE articles SET md5sum=?,modtime=? WHERE id=?", doc.Md5sum, doc.ModTime, doc.Id); err == nil {
		if c, err3 := r.RowsAffected(); err3 == nil {
			return c, true
		}
	} else {
		log.Printf("[INDEXSERVER] UPDATE DOC %s ERROR: %s", doc, err)
	}
	return -1, false
}

func (i *Indexer) Delete(path string) (int64, bool) {
	if r, err := i.db.Exec("DELETE FROM articles WHERE path=?", path); err == nil {
		if c, err3 := r.RowsAffected(); err3 == nil {
			return c, true
		}
	} else {
		log.Printf("[INDEXSERVER] DELETE path %s ERROR: %s", path, err)
	}
	return -1, false
}

// 启动时会进行一次遍历，交由数据库进行对比
func (i *Indexer) FirstRun() {
	if i.Force {
		dropIndexDb()
	}
	var count int
	// Print a list of all of the files and folders currently
	// being watched and their paths.
	for path, f := range i.w.WatchedFiles() {
		count++
		if a, ok := i.Find(path); ok && a != nil {
			if i.Force {
				b := NewDocumentByFileinfo(path, f)
				if changed := a.Compare(b); changed {
					b.Id = a.Id
					i.Update(b)
				}
				indexDoc(a)
			} else {
				b := NewDocumentByFileinfo(path, f)
				if changed := a.Compare(b); changed {
					b.Id = a.Id
					if _, ok := i.Update(b); ok {
						indexDoc(b)
					}
				}
			}
		} else {
			i.AddArticle(path)
		}
	}
	log.Printf("[INDEXSERVER] Startup Run Processed: %d files", count)
}

// 接收文件变化的事件消息，如有变化事件，则进行索引
func (i *Indexer) Run() {
	for {
		select {
		case <-i.ctx.Done():
			i.db.Close()
			return
		case event := <-i.w.Event:
			log.Println("[INDEXSERVER] ", event) // Print the event's info.
			// log.Printf("[INDEXSERVER] event:%s, path:%s", event.Op, event.Path)
			switch {
			case event.Op == watcher.Remove:
				// log.Printf("[INDEXSERVER] REMOVE EVENT: %s", event.Path)
				i.DelArticle(event.Path)
			case event.Op == watcher.Create:
				// log.Printf("[INDEXSERVER] CREATE EVENT: %s", event.Path)
				i.AddArticle(event.Path)
			case event.Op == watcher.Write:
				// log.Printf("[INDEXSERVER] WRITE EVENT: %s", event.Path)
				i.UpdateArticle(event.Path)
			case event.Op == watcher.Move:
				// log.Printf("[INDEXSERVER] Move EVENT: %s -> %s", event.OldPath, event.Path)
				i.MoveArticle(event.OldPath, event.Path)
			case event.Op == watcher.Rename:
				// log.Printf("[INDEXSERVER] Rename EVENT: %s -> %s", event.OldPath, event.Path)
				i.MoveArticle(event.OldPath, event.Path)
			}
		case err := <-i.w.Error:
			log.Println(err)
		case <-i.w.Closed:
			return
		}
	}
}

func (i *Indexer) AddArticle(path string) {
	// log.Printf("ADD: %s", path)
	doc := NewDocument(path)
	if _, ok := i.Insert(doc); ok {
		indexDoc(doc)
	}
}

func (i *Indexer) DelArticle(path string) {
	if DEBUG {
		log.Printf("[INDEXSERVER] DEL: %s", path)
	}
	if doc, ok := i.Find(path); ok {
		if _, ok := i.Delete(path); ok {
			removeDoc(doc)
		}
	}
}

func (i *Indexer) UpdateArticle(path string) {
	if DEBUG {
		log.Printf("[INDEXSERVER] UPDATE: %s", path)
	}
	if a, ok := i.Find(path); ok {
		b := NewDocument(path)
		if changed := a.Compare(b); changed {
			b.Id = a.Id
			if _, ok := i.Update(b); ok {
				indexDoc(b)
			}
		}
	} else {
		i.AddArticle(path)
	}
}

func (i *Indexer) MoveArticle(oldPath, path string) {
	if DEBUG {
		log.Printf("[INDEXSERVER] MOVE: %s -> %s", oldPath, path)
	}
	i.DelArticle(oldPath)
	i.AddArticle(path)
}

func NewDocument(path string) *Document {
	f, err1 := os.Open(path)
	if err1 != nil {
		return &Document{}
	}
	defer f.Close()

	finfo, err2 := f.Stat()
	if err2 != nil {
		return &Document{}
	}
	return &Document{
		Path:    path,
		Md5sum:  md5sum(path),
		ModTime: finfo.ModTime(),
	}
}

func NewDocumentByFileinfo(path string, finfo fs.FileInfo) *Document {
	return &Document{
		Path:    path,
		Md5sum:  md5sum(path),
		ModTime: finfo.ModTime(),
	}
}

type Document struct {
	Id      int64
	Path    string
	Md5sum  string
	ModTime time.Time
}

func (doc *Document) RelativePath() string {
	temp := strings.Replace(doc.Path, MdDir, "", 1)
	temp = strings.TrimRight(temp, ".md")
	return temp
}

func (doc *Document) Title() string {
	arr := strings.Split(doc.Path, "/")
	if len(arr) > 0 {
		return strings.TrimRight(arr[len(arr)-1], ".md")
	}
	return ""
}

// Return true while modtime and md5sum not equals
func (doc *Document) Compare(another *Document) bool {
	return doc.ModTime.Unix() != another.ModTime.Unix() || doc.Md5sum != another.Md5sum
}

func (doc *Document) String() string {
	return fmt.Sprintf("id %d path %s", doc.Id, doc.Path)
}

func indexDoc(doc *Document) bool {
	if dryIndex {
		return true
	}
	if DEBUG {
		log.Printf("[INDEXSERVER] indexing doc: %d at %s", doc.Id, doc.Path)
	}
	var article SDocument
	if content, err := os.ReadFile(doc.Path); err == nil {
		article = SDocument{
			Id:   doc.Id,
			Text: string(content),
			Document: SMetadata{
				Path:   doc.RelativePath(),
				Title:  doc.Title(),
				Md5sum: doc.Md5sum,
			},
		}
		data, _ := json.Marshal(article)
		body := bytes.NewBuffer(data)
		if r, err2 := http.Post(gofoundIndex, "application/json", body); err2 == nil {
			// log.Printf("[INDEXSERVER] status code: %d", r.StatusCode)
			return r.StatusCode == 200
		} else {
			log.Printf("[INDEXSERVER] post %s err: %s", gofoundIndex, err2)
		}
	} else {
		log.Printf("[INDEXSERVER] read file %s err: %s", doc.Path, err)
	}
	return false
}

func removeDoc(doc *Document) bool {
	if dryIndex {
		return true
	}
	if DEBUG {
		log.Printf("[INDEXSERVER] remove doc: %d at %s", doc.Id, doc.Path)
	}
	mapx := map[string]int64{
		"id": doc.Id,
	}
	data, _ := json.Marshal(mapx)
	body := bytes.NewBuffer(data)
	if r, err2 := http.Post(gofoundRemove, "application/json", body); err2 == nil {
		// log.Printf("[INDEXSERVER] status code: %d", r.StatusCode)
		return r.StatusCode == 200
	} else {
		log.Printf("[INDEXSERVER] post %s err: %s", gofoundRemove, err2)
	}
	return false
}

func dropIndexDb() bool {
	if dryIndex {
		return true
	}
	log.Printf("[INDEXSERVER] drop index database default ....")
	if r, err2 := http.Get(gofoundDrop); err2 == nil {
		// log.Printf("[INDEXSERVER] status code: %d", r.StatusCode)
		return r.StatusCode == 200
	} else {
		log.Printf("[INDEXSERVER] post %s err: %s", gofoundDrop, err2)
	}

	return false
}
