package main

import (
  "log"
  "strings"
  "net/http"
  "errors"
  "encoding/json"
  "os"
  "io"
  "io/ioutil"
  "os/exec"
  "time"
  "flag"
  "github.com/bootic/bootic_zmq"
  "path"
  "path/filepath"
  data "github.com/bootic/bootic_go_data"
)

const API_URL = "https://api.bootic.net/v1"

type AssetOrTemplate struct {
  Class []string
  Properties map[string]string
  Links map[string]map[string]string
}
type Entity struct {
  Class []string
  Properties map[string]string
  Entities map[string][]AssetOrTemplate
  Links map[string]map[string]string
}

type ThemeRequest struct {
  ShopId string
  token string
  Data *Entity
  conn *http.Client
}

func (req * ThemeRequest) Get () error {
  segments := []string{API_URL, "shops", req.ShopId, "theme.json"}
  url := strings.Join(segments, "/")
  log.Println("getting", url)

  request, _ := http.NewRequest("GET", url, nil)
  request.Header.Set("Authorization", "Bearer " + req.token)
  resp, err := req.conn.Do(request)
  if err != nil {
    return err
  }

  var data *Entity

  dec := json.NewDecoder(resp.Body)
  err = dec.Decode(&data)
  if err != nil {
    return err
  }
  req.Data = data
  log.Println("status", resp.StatusCode)

  if resp.StatusCode == 403 {
    return errors.New("Access Denied. You need an access token")
  }

  if resp.StatusCode == 401 {
    return errors.New("Access Denied. Your access token is expired or invalid")
  }

  return nil
}

func NewThemeRequest (shopId, token string) (req *ThemeRequest) {
  var t *Entity
  conn := &http.Client{}
  req = &ThemeRequest{shopId, token, t, conn}
  return req
}

type ThemeStore struct {
  dir string
  theme *ThemeRequest
  userNames string
}

func (store *ThemeStore) writeTemplates() {
  // Remove all templates. Git will remove missing ones and update the others.
  matches, _ := filepath.Glob(store.dir + "/*.*")
  for _, f := range matches {
    if path.Base(f) != ".git" { // mmm
      os.RemoveAll(f)
    }
  }

  for _, template := range store.theme.Data.Entities["templates"] {
    s := []string{template.Properties["name"], template.Properties["content_type"]}
    fileName := strings.Join(s, ".")
    dirAndFile := strings.Join([]string{store.dir, fileName}, "/")
    // write file
    err := ioutil.WriteFile(dirAndFile, []byte(template.Properties["body"]), 0644)
    if err != nil {
      log.Println("error: Could not write file", dirAndFile)
    }
  }
}

func (store *ThemeStore) writeAssets() {

  for _, asset := range store.theme.Data.Entities["assets"] {
    dirAndFile := strings.Join([]string{store.dir, "assets", asset.Properties["file_name"]}, "/")
    link := asset.Links["file"]
    if link == nil {
      link = asset.Links["image"]
    }

    // remove file if exists
    os.RemoveAll(dirAndFile)

    resp, err := http.Get(link["href"])
    defer resp.Body.Close()
    if err != nil {
      log.Println("error: asset not available", link["href"])
    }

    out, err := os.Create(dirAndFile)
    defer out.Close()
    if err != nil {
      log.Println("error: Could not create file ", dirAndFile)
    } else {
      _, err = io.Copy(out, resp.Body)
      if err != nil {
        log.Println("error: Could not download to ", dirAndFile)
      }
    }
  }
}

func (store *ThemeStore) Commit () {
  now := time.Now()
  cmdStr := "cd " + store.dir + " && git init . && git add --all . && git commit -m '" + store.userNames + ": " + now.String() + "'"
  cmd := exec.Command("bash", "-c", cmdStr )
  err := cmd.Run()
  if err != nil {
    log.Println("error: Could not commit, or nothing to commit.", store.dir)
  } else {
    log.Println("Changes commited to repository", store.dir)
  }
}

func (store *ThemeStore) Write() {
  assetsDir := strings.Join([]string{store.dir, "assets"}, "/")
  _ = os.RemoveAll(assetsDir) // just remove everything. Will be re-downloaded and git will remove missing ones.
  err := os.MkdirAll(assetsDir, 0700)
  if err != nil {
    log.Println("Could not write directories for shop " + store.dir)
    return
  }

  store.writeTemplates()
  store.writeAssets()

  store.Commit()
}

func (store *ThemeStore) DelayedWrite(subdomain string, duration time.Duration, doneChan chan string) {

  go func(){
    defer func() {
      if err := recover(); err != nil {
        log.Println("Goroutine failed 2:", subdomain, err)
      }
    }()

    time.Sleep(duration)

    err := store.theme.Get()
    if err != nil {
      doneChan <- subdomain // make sure we un-register this shop.
      panic(err)
    }
    go store.Write()
    doneChan <- subdomain
  }()
}

func NewThemeStore (dir string, theme *ThemeRequest, userNames string) (store *ThemeStore) {
  store = &ThemeStore{dir, theme, userNames}
  return
}

type TimedThemeWriter struct {
  dir string
  token string
  Notifier data.EventsChannel
  duration time.Duration
  stores map[string]*ThemeStore
}

func (writer *TimedThemeWriter) listen() {
  doneChan := make(chan string)

  for {
    select {
    case event := <- writer.Notifier:
      subdomain, _  := event.Get("data").Get("account").String()
      userNames, _  := event.Get("data").Get("user").String()
      shopId, _     := event.Get("data").Get("shop_id").String()
      hostname, _   := event.Get("data").Get("system").Get("host").String()
      log.Println("event:", subdomain, userNames, shopId, hostname)

      store := writer.stores[subdomain]

      if store == nil { // no store yet. Create.
        theme := NewThemeRequest(shopId, writer.token)
        store = NewThemeStore(writer.dir + subdomain, theme, userNames)
        store.DelayedWrite(subdomain, writer.duration, doneChan)
        writer.stores[subdomain] = store
        log.Println("register:", subdomain)
      } // else do nothing.
    case subdomain := <- doneChan:
      // A store is done writing. Un-register it so it can be registered again.
      log.Println("unregister:", subdomain)
      delete(writer.stores, subdomain)
    }
  }
}

func NewTimedThemeWriter(dir, token string, duration time.Duration) (writer *TimedThemeWriter) {
  writer = &TimedThemeWriter{
    dir: dir,
    token: token,
    Notifier: make(data.EventsChannel, 1),
    duration: duration,
    stores: make(map[string]*ThemeStore),
  }

  go writer.listen()

  return
}
// func (writer *TimedThemeWriter) tick() {
//   for {
//     select {
//     case <- writer.ticker.C:
//       log.Println("Tick!")
//     }
//   }
// }

func main () {
  var (
    zmqAddress      string
    dir             string
    interval        string
  )

  flag.StringVar(&zmqAddress, "zmqsocket", "tcp://127.0.0.1:6000", "ZMQ socket address to bind to")
  flag.StringVar(&dir, "dir", "./", "root directory to create Git repositories")
  flag.StringVar(&interval, "interval", "10s", "interval to save themes to Git")
  flag.Parse()

  duration, err := time.ParseDuration(interval)
  if err != nil {
    panic("INTERVAL cannot be parsed")
  }

  token := os.Getenv("BOOTIC_ACCESS_TOKEN")

  if(token == "") {
    log.Fatal("Please set the BOOTIC_ACCESS_TOKEN env variable")
  }

  // Setup ZMQ subscriber +++++++++++++++++++++++++++++++
  topic := "theme:"
  zmq, _ := booticzmq.NewZMQSubscriber(zmqAddress, topic)

  timedWriter := NewTimedThemeWriter(dir, token, duration)

  zmq.SubscribeToType(timedWriter.Notifier, "all")

  log.Println("ZMQ socket started on", zmqAddress, "topic '", topic, "'")
  log.Println("Git repos will be created in", dir)

  for {
    select {}
  }
}