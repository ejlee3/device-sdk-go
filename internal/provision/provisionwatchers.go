//
// Copyright (C) 2023 IOTech Ltd
// Copyright (C) 2023 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/edgexfoundry/device-sdk-go/v3/internal/cache"
	"github.com/edgexfoundry/go-mod-bootstrap/v3/bootstrap/container"
	"github.com/edgexfoundry/go-mod-bootstrap/v3/bootstrap/file"
	"github.com/edgexfoundry/go-mod-bootstrap/v3/bootstrap/interfaces"
	"github.com/edgexfoundry/go-mod-bootstrap/v3/di"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/clients/logger"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/common"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/dtos"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/dtos/requests"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/errors"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"net/url"
	"os"
	"path"
	"path/filepath"
)

func LoadProvisionWatchers(path string, dic *di.Container) errors.EdgeX {
	var addProvisionWatchersReq []requests.AddProvisionWatcherRequest
	var edgexErr errors.EdgeX
	if path == "" {
		return nil
	}

	lc := container.LoggingClientFrom(dic.Get)

	parsedUrl, err := url.Parse(path)
	if err != nil {
		return errors.NewCommonEdgeX(errors.KindServerError, "failed to parse provision watcher path as a URI", err)
	}
	if parsedUrl.Scheme == "http" || parsedUrl.Scheme == "https" {
		secretProvider := container.SecretProviderFrom(dic.Get)
		addProvisionWatchersReq, edgexErr = loadProvisionWatchersFromURI(path, parsedUrl.Redacted(), secretProvider, lc)
		if edgexErr != nil {
			return edgexErr
		}
	} else {
		addProvisionWatchersReq, edgexErr = loadProvisionWatchersFromFile(path, lc)
		if edgexErr != nil {
			return edgexErr
		}
	}
	if len(addProvisionWatchersReq) == 0 {
		return nil
	}

	pwc := container.ProvisionWatcherClientFrom(dic.Get)
	ctx := context.WithValue(context.Background(), common.CorrelationHeader, uuid.NewString()) //nolint: staticcheck
	_, edgexErr = pwc.Add(ctx, addProvisionWatchersReq)
	return edgexErr
}

func loadProvisionWatchersFromFile(path string, lc logger.LoggingClient) ([]requests.AddProvisionWatcherRequest, errors.EdgeX) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.NewCommonEdgeX(errors.KindServerError, "failed to create absolute path for provision watchers", err)
	}
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, errors.NewCommonEdgeX(errors.KindServerError, "failed to read directory for provision watchers", err)
	}

	if len(files) == 0 {
		return nil, nil
	}

	lc.Infof("Loading pre-defined provision watchers from %s(%d files found)", absPath, len(files))
	var addProvisionWatchersReq, processedProvisionWatchersReq []requests.AddProvisionWatcherRequest
	for _, file := range files {
		fullPath := filepath.Join(absPath, file.Name())
		processedProvisionWatchersReq = processProvisonWatcherFile(fullPath, fullPath, nil, lc)
		if len(processedProvisionWatchersReq) > 0 {
			addProvisionWatchersReq = append(addProvisionWatchersReq, processedProvisionWatchersReq...)
		}
	}
	return addProvisionWatchersReq, nil
}

func loadProvisionWatchersFromURI(inputURI, displayURI string, secretProvider interfaces.SecretProvider, lc logger.LoggingClient) ([]requests.AddProvisionWatcherRequest, errors.EdgeX) {
	bytes, err := file.Load(inputURI, secretProvider, lc)
	if err != nil {
		return nil, errors.NewCommonEdgeX(errors.KindServerError, fmt.Sprintf("failed to load Provision Watchers List from URI %s", displayURI), err)
	}

	if len(bytes) == 0 {
		return nil, nil
	}

	var files []string

	err = json.Unmarshal(bytes, &files)
	if err != nil {
		return nil, errors.NewCommonEdgeX(errors.KindServerError, "could not unmarshal Provision Watcher list contents", err)
	}

	if len(files) == 0 {
		return nil, nil
	}

	baseUrl, _ := path.Split(inputURI)
	lc.Infof("Loading pre-defined provision watchers from %s(%d files found)", displayURI, len(files))
	var addProvisionWatchersReq, processedProvisionWatchersReq []requests.AddProvisionWatcherRequest
	for _, file := range files {
		fullPath, parsedFullPath := GetFullAndParsedURI(baseUrl, file, "provison watcher", lc)
		if fullPath == "" || parsedFullPath == nil {
			continue
		}
		processedProvisionWatchersReq = processProvisonWatcherFile(fullPath, parsedFullPath.Redacted(), secretProvider, lc)
		if len(processedProvisionWatchersReq) > 0 {
			addProvisionWatchersReq = append(addProvisionWatchersReq, processedProvisionWatchersReq...)
		}
	}
	return addProvisionWatchersReq, nil
}

func processProvisonWatcherFile(fullPath, displayPath string, secretProvider interfaces.SecretProvider, lc logger.LoggingClient) []requests.AddProvisionWatcherRequest {
	var watcher dtos.ProvisionWatcher
	var addProvisionWatchersReq []requests.AddProvisionWatcherRequest

	fileType := GetFileType(fullPath)

	// if the file type is not yaml or json, it cannot be parsed - just return to not break the loop for other devices
	if fileType == OTHER {
		return nil
	}

	content, err := file.Load(fullPath, secretProvider, lc)
	if err != nil {
		lc.Errorf("Failed to read Provision Watcher from %s: %v", displayPath, err)
		return nil
	}

	switch fileType {
	case YAML:
		err = yaml.Unmarshal(content, &watcher)
		if err != nil {
			lc.Errorf("Failed to YAML decode Provision Watcher from %s: %v", displayPath, err)
			return nil
		}
	case JSON:
		err = json.Unmarshal(content, &watcher)
		if err != nil {
			lc.Errorf("Failed to JSON decode Provision Watcher from %s: %v", displayPath, err)
			return nil
		}
	}

	err = common.Validate(watcher)
	if err != nil {
		lc.Errorf("ProvisionWatcher %s validation failed: %v", watcher.Name, err)
		return nil
	}

	if _, ok := cache.ProvisionWatchers().ForName(watcher.Name); ok {
		lc.Infof("ProvisionWatcher %s exists, using the existing one", watcher.Name)
	} else {
		lc.Infof("ProvisionWatcher %s not found in Metadata, adding it...", watcher.Name)
		req := requests.NewAddProvisionWatcherRequest(watcher)
		addProvisionWatchersReq = append(addProvisionWatchersReq, req)
	}
	return addProvisionWatchersReq
}
