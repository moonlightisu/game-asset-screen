package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const maxUploadBytes = 12 << 20

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}

	pipeline := NewPipeline(NewInfraiClient(key), NewInfraiClient(key))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /submissions", submissionHandler(pipeline))
	log.Println("game asset queue listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func submissionHandler(pipeline *Pipeline) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart submission"})
			return
		}
		id := r.Header.Get("Idempotency-Key")
		caption := r.FormValue("caption")
		file, header, err := r.FormFile("image")
		if id == "" || caption == "" || err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "idempotency key, caption, and image are required"})
			return
		}
		defer file.Close()
		image, err := readUpload(file)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		decision, err := pipeline.Screen(r.Context(), Submission{ID: id, Caption: caption, Filename: header.Filename, Image: image})
		if err != nil {
			var apiErr *InfraiError
			if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
				writeJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "screening request failed"})
			return
		}
		writeJSON(w, http.StatusOK, decision)
	}
}

func readUpload(file io.Reader) ([]byte, error) {
	image, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if len(image) > maxUploadBytes {
		return nil, errors.New("image exceeds 12 MiB")
	}
	return image, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
