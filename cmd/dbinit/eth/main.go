package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go-dc-wallet/app"
	"go-dc-wallet/ethclient"
	"go-dc-wallet/heth"
	"go-dc-wallet/model"
	"go-dc-wallet/xenv"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/moremorefun/mcommon"
	"github.com/parnurzeal/gorequest"
)

func main() {
	xenv.EnvCreate()
	defer xenv.EnvDestroy()

	// 1. 初始化 t_app_config_int
	configIntRows := []*model.DBTAppConfigInt{
		{
			// 最小可用剩余地址数
			K: "min_free_address",
			V: 1000,
		},
		{
			// eth 确认延迟数
			K: "block_confirm_num",
			V: 15,
		},
		{
			// erc20 默认转账 gas
			K: "erc20_gas_use",
			V: 90000,
		},
	}
	_, err := model.SQLCreateManyTAppConfigInt(
		context.Background(),
		xenv.DbCon,
		configIntRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	// 2. 初始化 t_app_config_str
	// 获取可用地址
	ethAddressRows, err := app.SQLSelectTAddressKeyColByTagAndSymbol(
		context.Background(),
		xenv.DbCon,
		[]string{
			model.DBColTAddressKeyAddress,
		},
		-1,
		heth.CoinSymbol,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	var ethAddresses []string
	for _, ethAddressRow := range ethAddressRows {
		ethAddresses = append(ethAddresses, ethAddressRow.Address)
	}
	if len(ethAddresses) < 10 {
		// 创建可用地址
		ethAddresses, err = heth.CreateHotAddress(50)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	}

	if ethAddresses == nil {
		mcommon.Log.Errorf("ethAddresses nil")
		return
	}

	configStrRows := []*model.DBTAppConfigStr{
		{
			// eth 冷钱包地址
			K: "cold_wallet_address_eth",
			V: "",
		},
		{
			// eth 热钱包地址
			K: "hot_wallet_address_eth",
			V: ethAddresses[0],
		},
		{
			// erc20 零钱整理手续费 热钱包地址
			K: "fee_wallet_address_erc20",
			V: ethAddresses[1],
		},
		{
			// erc20 零钱整理手续费 热钱包地址 列表
			K: "fee_wallet_address_list_erc20",
			V: "",
		},
	}
	_, err = model.SQLCreateManyTAppConfigStr(
		context.Background(),
		xenv.DbCon,
		configStrRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	// 3. 初始化 t_app_config_token
	now := time.Now().Unix()
	configTokenRows := []*model.DBTAppConfigToken{
		{
			// erc20 token配置
			TokenAddress:  "0xdac17f958d2ee523a2206206994597c13d831ec7",
			TokenDecimals: 6,
			TokenSymbol:   "erc20_usdt",
			ColdAddress:   "",
			HotAddress:    ethAddresses[2],
			OrgMinBalance: "0.0",
			CreateTime:    now,
		},
	}
	_, err = model.SQLCreateManyTAppConfigToken(
		context.Background(),
		xenv.DbCon,
		configTokenRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	// 4. 初始化 t_app_status_int
	ethRPCBlockNum, err := ethclient.RPCBlockNumber(context.Background())
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	type StRespGasPrice struct {
		Fast        int64   `json:"fast"`
		Fastest     int64   `json:"fastest"`
		SafeLow     int64   `json:"safeLow"`
		Average     int64   `json:"average"`
		BlockTime   float64 `json:"block_time"`
		BlockNum    int64   `json:"blockNum"`
		Speed       float64 `json:"speed"`
		SafeLowWait float64 `json:"safeLowWait"`
		AvgWait     float64 `json:"avgWait"`
		FastWait    float64 `json:"fastWait"`
		FastestWait float64 `json:"fastestWait"`
	}
	gresp, body, errs := gorequest.New().
		Get("https://ethgasstation.info/api/ethgasAPI.json").
		Timeout(time.Second * 120).
		End()
	if errs != nil {
		mcommon.Log.Errorf("err: [%T] %s", errs[0], errs[0].Error())
		return
	}
	if gresp.StatusCode != http.StatusOK {
		// 状态错误
		mcommon.Log.Errorf("req status error: %d", gresp.StatusCode)
		return
	}
	var resp StRespGasPrice
	err = json.Unmarshal([]byte(body), &resp)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	ethToUserGasPrice := resp.Fast * int64(math.Pow10(8))
	ethToColdGasPrice := resp.Average * int64(math.Pow10(8))

	appStatusIntRows := []*model.DBTAppStatusInt{
		{
			// eth blocknum
			K: "eth_seek_num",
			V: ethRPCBlockNum,
		},
		{
			// eth blocknum
			K: "erc20_seek_num",
			V: ethRPCBlockNum,
		},
		{
			// eth 到冷钱包手续费
			K: "to_cold_gas_price_eth",
			V: ethToColdGasPrice,
		},
		{
			// eth 到用户手续费
			K: "to_user_gas_price_eth",
			V: ethToUserGasPrice,
		},
	}
	_, err = model.SQLCreateManyTAppStatusInt(
		context.Background(),
		xenv.DbCon,
		appStatusIntRows,
		true,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}

	// 5. 更新 t_app_config_str
	feeAddressValue, err := app.SQLGetTAppConfigStrValueByK(
		context.Background(),
		xenv.DbCon,
		"fee_wallet_address_erc20",
	)
	if err != nil && err != sql.ErrNoRows {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	feeAddressValue = strings.TrimSpace(feeAddressValue)
	feeAddressListValue, err := app.SQLGetTAppConfigStrValueByK(
		context.Background(),
		xenv.DbCon,
		"fee_wallet_address_list_erc20",
	)
	if err != nil && err != sql.ErrNoRows {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
	feeAddressListValue = strings.TrimSpace(feeAddressListValue)
	if feeAddressValue != "" && !strings.Contains(feeAddressListValue, feeAddressValue) {
		if feeAddressListValue == "" {
			feeAddressListValue = feeAddressValue
		} else {
			feeAddressListValue += fmt.Sprintf(",%s", feeAddressValue)
		}
	}
	_, err = app.SQLUpdateTAppConfigStrByK(
		context.Background(),
		xenv.DbCon,
		&model.DBTAppConfigStr{
			K: "fee_wallet_address_list_erc20",
			V: feeAddressListValue,
		},
	)
	if err != nil && err != sql.ErrNoRows {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return
	}
}
