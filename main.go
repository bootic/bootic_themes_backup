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
}

func (store *ThemeStore) writeTemplates() {
  for _, template := range store.theme.Data.Entities["templates"] {
    s := []string{template.Properties["name"], template.Properties["content_type"]}
    fileName := strings.Join(s, ".")
    dirAndFile := strings.Join([]string{store.dir, fileName}, "/")
    log.Println(dirAndFile)
    // remove file if exists
    os.RemoveAll(dirAndFile)
    // write file
    err := ioutil.WriteFile(dirAndFile, []byte(template.Properties["body"]), 0644)
    if err != nil {
      log.Println("error: Could not write file", dirAndFile)
    }
  }
}

func (store *ThemeStore) writeAssets() chan int {
  c := make(chan int)

  for i, asset := range store.theme.Data.Entities["assets"] {
    go func (asset AssetOrTemplate, i int, c chan int) {
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
        log.Fatal("error: Could not create file", dirAndFile)
      }

      _, err = io.Copy(out, resp.Body)
      if err != nil {
        log.Fatal("error: Could not download to", dirAndFile)
      }
      c <- 1
      log.Println(dirAndFile, i)
    }(asset, i, c)
  }
  return c
}

func (store *ThemeStore) Commit () {
  now := time.Now()
  cmdStr := "cd " + store.dir + " && git init . && git add . && git commit -m '" + now.String() + "'"
  cmd := exec.Command("bash", "-c", cmdStr )
  err := cmd.Run()
  if err != nil {
    log.Println("error: Could not commit, or nothing to commit.")
  } else {
    log.Println("Changes commited to repository")
  }
}

func (store *ThemeStore) Write() {
  err := os.MkdirAll(strings.Join([]string{store.dir, "assets"}, "/"), 0700)
  if err != nil {
    log.Fatal("Could not write directories for shop " + store.dir)
  }
  store.writeTemplates()

  assetsCount := len(store.theme.Data.Entities["assets"])
  it := 0
  c := store.writeAssets()
  for {
    select {
    case <- c:
      it = it + 1
      if it == assetsCount {
        store.Commit()
      }
    }
  }
}

func NewThemeStore (dir string, theme *ThemeRequest) (store *ThemeStore) {
  store = &ThemeStore{dir, theme}
  return
}

func main () {
  var (
    zmqAddress      string
    dir             string
  )

  flag.StringVar(&zmqAddress, "zmqsocket", "tcp://127.0.0.1:6000", "ZMQ socket address to bind to")
  flag.StringVar(&dir, "dir", "./", "root directory to create Git repositories")
  flag.Parse()

  token := os.Getenv("BOOTIC_ACCESS_TOKEN")

  if(token == "") {
    log.Fatal("Please set the BOOTIC_ACCESS_TOKEN env variable")
  }

  // Setup ZMQ subscriber +++++++++++++++++++++++++++++++
  topic := "theme:"//"theme:create_asset theme:update_asset theme:destroy_asset dynamicTeplate"
  zmq, _ := booticzmq.NewZMQSubscriber(zmqAddress, topic)

  zmq.SubscribeFunc(func(event *data.Event) {
    subdomain, _ := event.Get("data").Get("account").String()
    shopId, _ := event.Get("data").Get("shop_id").String()
    theme := NewThemeRequest(shopId, token)
    err := theme.Get()
    if err != nil {
      log.Fatal(err)
    }

    store := NewThemeStore(dir + subdomain, theme)
    store.Write()
  })

  log.Println("ZMQ socket started on", zmqAddress, "topic '", topic, "'")
  log.Println("Git repos will be created in", dir)

  for {
    select {}
  }
}