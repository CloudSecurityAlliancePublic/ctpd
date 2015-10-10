//    Copyright 2015 Cloud Security Alliance EMEA (cloudsecurityalliance.org)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ctp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
    "sync"
    "gopkg.in/mgo.v2"
    "gopkg.in/mgo.v2/bson"
    "io"
)

type SessionId uint

type ApiContext struct {
	Configuration Configuration
	CtpBase       Link
	CtpPath       string
	Signature     string
	Params        []string
    QueryParam    string
    Id            SessionId
    Session       *mgo.Session
    AccessTags    Tags
}

type HandlerFunc func(http.ResponseWriter, *http.Request, *ApiContext)

var mutexCounter sync.Mutex
var contextCounter uint = 0

func NewApiContext(r *http.Request, conf Configuration) (*ApiContext, error) {
	c := new(ApiContext)
	if r.TLS != nil {
		c.CtpBase = Link("https://" + r.Host + conf["basepath"])
	} else {
		c.CtpBase = Link("http://" + r.Host + conf["basepath"])
	}
	c.CtpPath = conf["basepath"]
	c.Configuration = conf
    signature, params, xparam := RequestSignature(conf["basepath"], r)
	c.Signature = signature
	c.Params = params
    c.QueryParam = xparam
    mutexCounter.Lock()
    contextCounter++
    c.Id = SessionId(contextCounter)
    mutexCounter.Unlock()

    session, err := mgo.Dial(conf["databaseurl"])
    if err!=nil {
        Log(c,"Failed to connect to database %s: %s",conf["databaseurl"],err.Error())
        return c, err
    }
    c.Session = session
    c.AccessTags = AnybodyAccess
	return c, nil
}

func (c *ApiContext) Close() {
	if c.Session!=nil {
        c.Session.Close()
    }
}

func load_access_tags(session *mgo.Session, key string) ([]string, bool) {
    var access Access

    query := session.DB("ctp").C("access").Find(bson.M{"token": key})

    count, err := query.Count()
    if err != nil {
        return nil, false
    }
    if count == 0 {
        return nil, false
    }

    if err = query.One(&access); err != nil {
        return nil, false
    }
    if access.AccessTags == nil {
        return nil, false
    }
    return access.AccessTags, true
}

func (c *ApiContext) VerifyAccessTags(w http.ResponseWriter, dest Tags) bool {
    if MatchTags(c.AccessTags,dest) {
        return true
    }
    Log(c,"context-tags=%s / dest-tags=%s", c.AccessTags.String(),dest.String())
    RenderErrorResponse(w,c,NewHttpError(http.StatusUnauthorized,"You do not have permission to access this resource"))
    return false
}

func (c *ApiContext) AuthenticateClient(w http.ResponseWriter, r *http.Request) bool {
    token, ok := BearerAuth(r)

    if ok {
        if accesstags, ok := load_access_tags(c.Session,token); ok {
            c.AccessTags = accesstags
            return true
        }
        Log(c,"Could not find token '%s'",token);
    } else {
        Log(c,"Failed to parse http authorization header (%s)",r.Header.Get("Authorization"))
    }

    w.Header().Set("WWW-Authenticate", `Bearer realm="ctp api"`)

    RenderErrorResponse(w,c,NewHttpError(http.StatusUnauthorized,"Malformed, incorrect or missing CTP API token"))
    return false
}

func LoadResource(c *ApiContext, category string, id Base64Id, resource interface{}) bool {
    query := c.Session.DB("ctp").C(category).FindId(id)

    count, err := query.Count()
    if err != nil {
        return false
    }
    if count == 0 {
        return false
    }

    err = query.One(resource)
    if err != nil {
        return false
    }
    return true
}

func ParseResource(body io.ReadCloser, resource interface{}) bool {
    input := new(bytes.Buffer)

    if _, err := input.ReadFrom(body); err != nil {
        return false
    }

    if err := json.Unmarshal(input.Bytes(), resource); err != nil {
        return false
    }

    return true
}

func CreateResource(c *ApiContext, category string, resource interface{}) bool {
    if err := c.Session.DB("ctp").C(category).Insert(resource); err != nil {
        return false
    }
    return true
}

func UpdateResource(c *ApiContext, category string, id Base64Id, resource interface{}) bool {
    if err := c.Session.DB("ctp").C(category).UpdateId(id, resource); err != nil {
        return false
    }
    return true
}

func DeleteResource(c *ApiContext, category string, id Base64Id) bool {
    if err := c.Session.DB("ctp").C(category).RemoveId(id); err != nil {
        return false
    }
    return true
}

func UpdateResourcePart(c *ApiContext, category string, id Base64Id, part string, resource interface{}) bool {
    if err := c.Session.DB("ctp").C(category).UpdateId(id, bson.M{"$set": bson.M{part: resource}}); err != nil {
        return false
    }
    return true
}



/////////////////////////

func RenderErrorResponse(w http.ResponseWriter, context *ApiContext, err *HttpError) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(err.StatusCode())
	fmt.Fprintf(w, `{ "error": "%s" }`, err.Error())
    Log(context,"Error: %s", err.Error())
}

func RenderJsonResponse(w http.ResponseWriter, context *ApiContext, code int, item interface{}) {
	var jsonRendering []byte
    var err error

    if item!=nil {
        jsonRendering, err = json.MarshalIndent(item, "", "  ")
        if err != nil {
            RenderErrorResponse(w, context, NewInternalServerError(err))
            return
        }
        jsonRendering = bytes.Replace(jsonRendering, []byte("\\u003c"), []byte("<"), -1)
        jsonRendering = bytes.Replace(jsonRendering, []byte("\\u003e"), []byte(">"), -1)
        jsonRendering = bytes.Replace(jsonRendering, []byte("\\u0026"), []byte("&"), -1)
    }
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(jsonRendering)
    Log(context,"Succes: %d bytes content, status code=%d ", len(jsonRendering), code)
}


