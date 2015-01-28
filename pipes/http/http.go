package http

import (
	"github.com/creamdog/aproxy/mappings"
	"github.com/creamdog/aproxy/cache"
	"io"
	"log"
	"net/http"
	"strings"
	"encoding/json"
	"bytes"
	"fmt"
	"time"
)

type HttpPipe struct {
	cache cache.CacheClient
}

type CachedResponse struct {
	Header map[string][]string
	StatusCode int
	Body string
	Expires int
	Key string
}

func New(cacheClient cache.CacheClient) *HttpPipe {
	return &HttpPipe{
		cache : cacheClient,
	}
}

func (pipe *HttpPipe) Pipe(mapping *mappings.RequestMapping, w http.ResponseWriter) {

	log.Printf("%v %v, %v, cacheKey: %s", mapping.Verb, mapping.Uri, mapping.Headers, mapping.CacheKey)

	w.Header().Set("X-AProxy-Version", "0.1")
	_, notransform := (*mapping.Data)["query"].(map[string]interface{})["_notransform"]
	_, nocache := (*mapping.Data)["query"].(map[string]interface{})["_nocache"]

	if nocache {
		mapping.CacheKey = ""		
	}

	if len(mapping.CacheKey) > 0 {
		var cacheResponse CachedResponse
		if ok, err := pipe.cache.Get(mapping.CacheKey, &cacheResponse); ok {
			log.Printf("cache hit: %v", mapping.CacheKey)
			for key, value := range cacheResponse.Header {
				w.Header().Set(key, value[0])
			}

			w.Header().Set("X-Cache-Hit", "true")
			w.Header().Set("X-Cache-Key", cacheResponse.Key)
			w.Header().Set("X-Cache-Expiration-Seconds", fmt.Sprintf("%d", int64(cacheResponse.Expires) - time.Now().Unix()))

			w.WriteHeader(cacheResponse.StatusCode)
			fmt.Fprint(w, cacheResponse.Body)
			return
		} else if err != nil {
			log.Printf("%v", err)
		} else {
			log.Printf("cache miss '%s'", mapping.CacheKey)
		}
	}


	request, err := http.NewRequest(mapping.Verb, mapping.Uri, strings.NewReader(mapping.Body))

	

	if err != nil {
		http.Error(w, err.Error(), 503)
	}
	request.ContentLength = int64(len(mapping.Body))

	//log.Printf("request.ContentLength: %d, mapping.Body: %v", request.ContentLength, mapping.Body)

	for key, value := range mapping.Headers {
		request.Header[key] = []string{value}
	}

	request.Header["Transfer-Encoding"] = []string{""}

	

	client := &http.Client{}
	if response, err := client.Do(request); err != nil {
		http.Error(w, err.Error(), 500)
	} else {

		defer response.Body.Close()

		responseBody := ""
		responseBodyRead := false

		readResponseBody := func() ([]byte, error) {
			responseBodyRead = true
			readBuffer := make([]byte, 1024)
			buffer := make([]byte, 0)
			read := true
			//max := 1 * 1024 * 1024
			for read {
				/*
				if len(buffer) >= max {
					http.Error(w, fmt.Errorf("transform: maximum input site exceeded %d bytes", max).Error(), 500)
					return
				}
				*/
				read = false
				num, err := response.Body.Read(readBuffer)
				if err != nil {
					if err.Error() == "EOF" {
						buffer = append(buffer, readBuffer[:num]...)
						break
					}
					return nil, err
				}
				//log.Printf("read %d bytes", num)
				if num > 0 {
					buffer = append(buffer, readBuffer[:num]...)
					read = true
				}
			}
			return buffer, nil
		}

		
		if mapping.CompiledTransform != nil && !notransform {

			buffer, err := readResponseBody()
			if err != nil {
				http.Error(w, err.Error(), 500)
			}

			//log.Printf("buffer[%d]: %v", len(buffer), string(buffer))

			responseData := map[string]interface{}{}

			if mapping.Mapping.Target.Transform.Type == "json" {
				if err := json.Unmarshal(buffer, &responseData); err != nil {
					http.Error(w, err.Error() + " : " + string(buffer), 500)
					return
				}
			} else if mapping.Mapping.Target.Transform.Type == "regexp" {
				re := mapping.Mapping.Target.Transform.Regexp.FindStringSubmatch(string(buffer))
				names := mapping.Mapping.Target.Transform.Regexp.SubexpNames()
				if re != nil {
					for i, n := range re {
						if len(names[i]) > 0 {
							responseData[names[i]] = n
						}
					}
				}
			}

			//log.Printf("responseData: %v", responseData)

			data := map[string]interface{}{
				"data" : responseData,
			}
			for key, value := range *mapping.Data {
				data[key] = value
			}

			var renderBuffer bytes.Buffer
			if err := mapping.CompiledTransform.Execute(&renderBuffer, data); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			responseBody = renderBuffer.String()
		} 

		for key, value := range response.Header {
			w.Header().Set(key, value[0])
		}
		w.WriteHeader(response.StatusCode)


		if len(mapping.CacheKey) > 0 && !responseBodyRead {
			buffer, err := readResponseBody()
			if err != nil {
				http.Error(w, err.Error(), 500)
			}
			responseBody = string(buffer)
		}

		if responseBodyRead {

			if len(mapping.CacheKey) > 0 {
				cachedResponse := CachedResponse{
					Header : response.Header,
					StatusCode: response.StatusCode,
					Body: responseBody,
					Expires: int(time.Now().Unix()) + mapping.Mapping.Caching.Seconds,
					Key: mapping.CacheKey,
				}
				pipe.cache.Set(mapping.CacheKey, cachedResponse.Expires, cachedResponse)
			}

			fmt.Fprint(w, responseBody)
		} else {
			io.Copy(w, response.Body)
		}
	}
}
