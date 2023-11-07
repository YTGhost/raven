/*
 * Copyright 2023 The OpenYurt Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package utils

import (
	"fmt"

	"github.com/ccding/go-stun/stun"
	"github.com/vdobler/ht/errorlist"
)

var (
	stunAPIs = [...]string{
		"stun.qq.com:3478",
		"stun.miwifi.com:3478",
	}
	stunClient *stun.Client
)

func init() {
	stunClient = stun.NewClient()
	stunClient.SetLocalPort(4500)
}

func GetNATType() (string, error) {
	errList := errorlist.List{}
	for _, api := range stunAPIs {
		stunClient.SetServerAddr(api)
		natBehavior, err := stunClient.BehaviorTest()
		if err == nil {
			return natBehavior.NormalType(), nil
		}
		errList = errList.Append(err)
	}
	return "", fmt.Errorf("error get nat type by any of the apis[%v]: %s", stunAPIs, errList.AsError())
}

func GetPublicPort() (int, error) {
	errList := errorlist.List{}
	for _, api := range stunAPIs {
		stunClient.SetServerAddr(api)
		_, host, err := stunClient.Discover()
		if err == nil {
			return int(host.Port()), nil
		}
		errList = errList.Append(err)
	}
	return 0, fmt.Errorf("error get public port by any of the apis[%v]: %s", stunAPIs, errList.AsError())
}
