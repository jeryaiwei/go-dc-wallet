package heth

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go-dc-wallet/app"
	"go-dc-wallet/ethclient"
	"go-dc-wallet/model"
	"go-dc-wallet/xenv"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/moremorefun/mcommon"
	"github.com/parnurzeal/gorequest"

	"github.com/ethereum/go-ethereum/accounts/abi"

	"github.com/gin-gonic/gin"

	"github.com/ethereum/go-ethereum/rlp"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/ethereum/go-ethereum/crypto"
)

func genAddressAndAesKey() (string, string, error) {
	// 生成私钥
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		return "", "", err
	}
	privateKeyBytes := crypto.FromECDSA(privateKey)
	privateKeyStr := hexutil.Encode(privateKeyBytes)
	// 加密密钥
	privateKeyStrEn, err := mcommon.AesEncrypt(privateKeyStr, xenv.Cfg.AESKey)
	if err != nil {
		return "", "", err
	}
	// 获取地址
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", "", errors.New("can't change public key")
	}
	// 地址全部储存为小写方便处理
	address := AddressBytesToStr(crypto.PubkeyToAddress(*publicKeyECDSA))
	return address, privateKeyStrEn, nil
}

// CreateHotAddress 创建自用地址
func CreateHotAddress(num int64) ([]string, error) {
	var rows []*model.DBTAddressKey
	var addresses []string
	// 遍历差值次数
	for i := int64(0); i < num; i++ {
		address, privateKeyStrEn, err := genAddressAndAesKey()
		if err != nil {
			return nil, err
		}
		// 存入待添加队列
		rows = append(rows, &model.DBTAddressKey{
			Symbol:  CoinSymbol,
			Address: address,
			Pwd:     privateKeyStrEn,
			UseTag:  -1,
		})
		addresses = append(addresses, address)
	}
	// 一次性将生成的地址存入数据库
	_, err := model.SQLCreateManyTAddressKey(
		context.Background(),
		xenv.DbCon,
		rows,
		true,
	)
	if err != nil {
		return nil, err
	}
	return addresses, nil
}

// CheckAddressFree 检测是否有充足的备用地址
func CheckAddressFree() {
	lockKey := "EthCheckAddressFree"
	app.LockWrap(lockKey, func() {
		// 获取配置 允许的最小剩余地址数
		minFreeCount, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"min_free_address",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取当前剩余可用地址数
		freeCount, err := app.SQLGetTAddressKeyFreeCount(
			context.Background(),
			xenv.DbCon,
			CoinSymbol,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 如果数据库中剩余可用地址小于最小允许可用地址
		if freeCount < minFreeCount {
			var rows []*model.DBTAddressKey
			// 遍历差值次数
			for i := int64(0); i < minFreeCount-freeCount; i++ {
				address, privateKeyStrEn, err := genAddressAndAesKey()
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 存入待添加队列
				rows = append(rows, &model.DBTAddressKey{
					Symbol:  CoinSymbol,
					Address: address,
					Pwd:     privateKeyStrEn,
					UseTag:  0,
				})
			}
			// 一次性将生成的地址存入数据库
			_, err = model.SQLCreateManyTAddressKey(
				context.Background(),
				xenv.DbCon,
				rows,
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
	})
}

// CheckBlockSeek 检测到账
func CheckBlockSeek() {
	lockKey := "EthCheckBlockSeek"
	app.LockWrap(lockKey, func() {
		// 获取配置 延迟确认数
		confirmValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"block_confirm_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取状态 当前处理完成的最新的block number
		seekValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"eth_seek_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// rpc 获取当前最新区块数
		rpcBlockNum, err := ethclient.RPCBlockNumber(context.Background())
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		startI := seekValue + 1
		endI := rpcBlockNum - confirmValue + 1
		if startI < endI {
			// 手续费钱包列表
			feeAddressValue, err := app.SQLGetTAppConfigStrValueByK(
				context.Background(),
				xenv.DbCon,
				"fee_wallet_address_list_erc20",
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			addresses := strings.Split(feeAddressValue, ",")
			var feeAddresses []string
			for _, address := range addresses {
				if address == "" {
					continue
				}
				feeAddresses = append(feeAddresses, address)
			}
			// 遍历获取需要查询的block信息
			for i := startI; i < endI; i++ {
				// rpc获取block信息
				//mcommon.Log.Debugf("eth check block: %d", i)
				rpcBlock, err := ethclient.RPCBlockByNum(context.Background(), i)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 接收地址列表
				var toAddresses []string
				// map[接收地址] => []交易信息
				toAddressTxMap := make(map[string][]*types.Transaction)
				// 遍历block中的tx
				for _, rpcTx := range rpcBlock.Transactions() {
					// 转账数额大于0 and 不是创建合约交易
					if rpcTx.Value().Int64() > 0 && rpcTx.To() != nil {
						msg, err := rpcTx.AsMessage(types.NewEIP155Signer(rpcTx.ChainId()))
						if err != nil {
							mcommon.Log.Errorf("AsMessage err: [%T] %s", err, err.Error())
							return
						}
						if mcommon.IsStringInSlice(feeAddresses, AddressBytesToStr(msg.From())) {
							// 如果打币地址在手续费热钱包地址则不处理
							continue
						}
						toAddress := AddressBytesToStr(*(rpcTx.To()))
						toAddressTxMap[toAddress] = append(toAddressTxMap[toAddress], rpcTx)
						if !mcommon.IsStringInSlice(toAddresses, toAddress) {
							toAddresses = append(toAddresses, toAddress)
						}
					}
				}
				// 从db中查询这些地址是否是冲币地址中的地址
				dbAddressRows, err := app.SQLSelectTAddressKeyColByAddress(
					context.Background(),
					xenv.DbCon,
					[]string{
						model.DBColTAddressKeyAddress,
						model.DBColTAddressKeyUseTag,
					},
					toAddresses,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 待插入数据
				var dbTxRows []*model.DBTTx
				// map[接收地址] => 产品id
				addressProductMap := make(map[string]int64)
				for _, dbAddressRow := range dbAddressRows {
					addressProductMap[dbAddressRow.Address] = dbAddressRow.UseTag
				}
				// 时间
				now := time.Now().Unix()
				// 遍历数据库中有交易的地址
				for _, dbAddressRow := range dbAddressRows {
					if dbAddressRow.UseTag < 0 {
						continue
					}
					// 获取地址对应的交易列表
					txes := toAddressTxMap[dbAddressRow.Address]
					for _, tx := range txes {
						msg, err := tx.AsMessage(types.NewEIP155Signer(tx.ChainId()))
						if err != nil {
							mcommon.Log.Errorf("AsMessage err: [%T] %s", err, err.Error())
							return
						}
						fromAddress := AddressBytesToStr(msg.From())
						toAddress := AddressBytesToStr(*(tx.To()))
						balanceReal, err := WeiBigIntToEthStr(tx.Value())
						if err != nil {
							mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
							return
						}
						dbTxRows = append(dbTxRows, &model.DBTTx{
							ProductID:    addressProductMap[toAddress],
							TxID:         tx.Hash().String(),
							FromAddress:  fromAddress,
							ToAddress:    toAddress,
							BalanceReal:  balanceReal,
							CreateTime:   now,
							HandleStatus: app.TxStatusInit,
							HandleMsg:    "",
							HandleTime:   now,
							OrgStatus:    app.TxOrgStatusInit,
							OrgMsg:       "",
							OrgTime:      now,
						})
					}
				}
				// 插入交易数据
				_, err = model.SQLCreateManyTTx(
					context.Background(),
					xenv.DbCon,
					dbTxRows,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新检查到的最新区块数
				_, err = app.SQLUpdateTAppStatusIntByKGreater(
					context.Background(),
					xenv.DbCon,
					&model.DBTAppStatusInt{
						K: "eth_seek_num",
						V: i,
					},
				)
				if err != nil {
					mcommon.Log.Errorf("SQLUpdateTAppStatusIntByK err: [%T] %s", err, err.Error())
					return
				}
			}
		}
	})
}

// CheckAddressOrg 零钱整理到冷钱包
func CheckAddressOrg() {
	lockKey := "EthCheckAddressOrg"
	app.LockWrap(lockKey, func() {
		// 获取冷钱包地址
		coldAddressValue, err := app.SQLGetTAppConfigStrValueByK(
			context.Background(),
			xenv.DbCon,
			"cold_wallet_address_eth",
		)
		if err != nil {
			mcommon.Log.Warnf("SQLGetTAppConfigInt err: [%T] %s", err, err.Error())
			return
		}
		coldAddress, err := StrToAddressBytes(coldAddressValue)
		if err != nil {
			mcommon.Log.Errorf("eth organize cold address err: [%T] %s", err, err.Error())
			return
		}
		// 开启事物
		isComment := false
		dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		defer func() {
			if !isComment {
				_ = dbTx.Rollback()
			}
		}()
		// 获取待整理的交易列表
		txRows, err := app.SQLSelectTTxColByOrgForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTTxID,
				model.DBColTTxToAddress,
				model.DBColTTxBalanceReal,
			},
			app.TxOrgStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(txRows) <= 0 {
			// 没有要处理的信息
			return
		}
		// 获取gap price
		gasPriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			dbTx,
			"to_cold_gas_price_eth",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		gasPrice := gasPriceValue
		gasLimit := int64(21000)
		feeValue := big.NewInt(gasLimit * gasPrice)
		// chain id
		chainID, err := ethclient.RPCNetworkID(context.Background())
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 当前时间
		now := time.Now().Unix()
		// 将待整理地址按地址做归并处理
		type OrgInfo struct {
			RowIDs  []int64  // db t_tx.id
			Balance *big.Int // 金额
		}
		// addressMap map[地址] = []整理信息
		addressMap := make(map[string]*OrgInfo)
		// addresses 需要整理的地址列表
		var addresses []string
		for _, txRow := range txRows {
			info := addressMap[txRow.ToAddress]
			if info == nil {
				info = &OrgInfo{
					RowIDs:  []int64{},
					Balance: new(big.Int),
				}
				addressMap[txRow.ToAddress] = info
			}
			info.RowIDs = append(info.RowIDs, txRow.ID)
			txWei, err := EthStrToWeiBigInit(txRow.BalanceReal)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			info.Balance.Add(info.Balance, txWei)

			if !mcommon.IsStringInSlice(addresses, txRow.ToAddress) {
				addresses = append(addresses, txRow.ToAddress)
			}
		}
		// 获取地址私钥
		addressPKMap, err := GetPKMapOfAddresses(
			context.Background(),
			dbTx,
			addresses,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		for address, info := range addressMap {
			// 获取私钥
			privateKey, ok := addressPKMap[address]
			if !ok {
				mcommon.Log.Errorf("no key of: %s", address)
				continue
			}
			// 获取nonce值
			nonce, err := GetNonce(dbTx, address)
			if err != nil {
				mcommon.Log.Errorf("GetNonce err: [%T] %s", err, err.Error())
				return
			}
			// 发送数量
			sendBalance := new(big.Int)
			sendBalance.Sub(info.Balance, feeValue)
			if sendBalance.Cmp(new(big.Int)) <= 0 {
				// 数额不足
				continue
			}
			sendBalanceReal, err := WeiBigIntToEthStr(sendBalance)
			if err != nil {
				mcommon.Log.Errorf("GetNonce err: [%T] %s", err, err.Error())
				return
			}
			// 生成tx
			var data []byte
			tx := types.NewTransaction(
				uint64(nonce),
				coldAddress,
				sendBalance,
				uint64(gasLimit),
				big.NewInt(gasPrice),
				data,
			)
			// 签名
			signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainID)), privateKey)
			if err != nil {
				mcommon.Log.Warnf("RPCNetworkID err: [%T] %s", err, err.Error())
				return
			}
			ts := types.Transactions{signedTx}
			rawTxBytes := ts.GetRlp(0)
			rawTxHex := hex.EncodeToString(rawTxBytes)
			txHash := strings.ToLower(signedTx.Hash().Hex())
			// 创建存入数据
			var sendRows []*model.DBTSend
			for rowIndex, rowID := range info.RowIDs {
				if rowIndex == 0 {
					// 只有第一条数据需要发送，其余数据为占位数据
					sendRows = append(sendRows, &model.DBTSend{
						RelatedType:  app.SendRelationTypeTx,
						RelatedID:    rowID,
						TxID:         txHash,
						FromAddress:  address,
						ToAddress:    coldAddressValue,
						BalanceReal:  sendBalanceReal,
						Gas:          gasLimit,
						GasPrice:     gasPrice,
						Nonce:        nonce,
						Hex:          rawTxHex,
						CreateTime:   now,
						HandleStatus: app.SendStatusInit,
						HandleMsg:    "",
						HandleTime:   now,
					})
				} else {
					// 占位数据
					sendRows = append(sendRows, &model.DBTSend{
						RelatedType:  app.SendRelationTypeTx,
						RelatedID:    rowID,
						TxID:         txHash,
						FromAddress:  address,
						ToAddress:    coldAddressValue,
						BalanceReal:  "0",
						Gas:          0,
						GasPrice:     0,
						Nonce:        -1,
						Hex:          "",
						CreateTime:   now,
						HandleStatus: app.SendStatusInit,
						HandleMsg:    "",
						HandleTime:   now,
					})
				}
			}
			// 插入发送数据
			_, err = model.SQLCreateManyTSend(
				context.Background(),
				dbTx,
				sendRows,
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 更改tx整理状态
			_, err = app.SQLUpdateTTxOrgStatusByIDs(
				context.Background(),
				dbTx,
				info.RowIDs,
				model.DBTTx{
					OrgStatus: app.TxOrgStatusHex,
					OrgMsg:    "hex",
					OrgTime:   now,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		// 提交事物
		err = dbTx.Commit()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		isComment = true
	})
}

// CheckRawTxSend 发送交易
func CheckRawTxSend() {
	lockKey := "EthCheckRawTxSend"
	app.LockWrap(lockKey, func() {
		// 获取待发送的数据
		sendRows, err := app.SQLSelectTSendColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTSendID,
				model.DBColTSendTxID,
				model.DBColTSendHex,
				model.DBColTSendRelatedType,
				model.DBColTSendRelatedID,
			},
			app.SendStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 首先单独处理提币，提取提币通知要使用的数据
		var withdrawIDs []int64
		for _, sendRow := range sendRows {
			switch sendRow.RelatedType {
			case app.SendRelationTypeWithdraw:
				if !mcommon.IsIntInSlice(withdrawIDs, sendRow.RelatedID) {
					withdrawIDs = append(withdrawIDs, sendRow.RelatedID)
				}
			}
		}
		withdrawMap, err := app.SQLGetWithdrawMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawProductID,
				model.DBColTWithdrawOutSerial,
				model.DBColTWithdrawToAddress,
				model.DBColTWithdrawSymbol,
				model.DBColTWithdrawBalanceReal,
			},
			withdrawIDs,
		)
		// 产品
		var productIDs []int64
		for _, withdrawRow := range withdrawMap {
			if !mcommon.IsIntInSlice(productIDs, withdrawRow.ProductID) {
				productIDs = append(productIDs, withdrawRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		// 执行发送
		var sendIDs []int64
		var txIDs []int64
		var erc20TxIDs []int64
		var erc20TxFeeIDs []int64
		withdrawIDs = []int64{}
		// 通知数据
		var notifyRows []*model.DBTProductNotify
		now := time.Now().Unix()
		var sendTxHashes []string
		onSendOk := func(sendRow *model.DBTSend) error {
			// 将发送成功和占位数据计入数组
			if !mcommon.IsIntInSlice(sendIDs, sendRow.ID) {
				sendIDs = append(sendIDs, sendRow.ID)
			}
			switch sendRow.RelatedType {
			case app.SendRelationTypeTx:
				if !mcommon.IsIntInSlice(txIDs, sendRow.RelatedID) {
					txIDs = append(txIDs, sendRow.RelatedID)
				}
			case app.SendRelationTypeWithdraw:
				if !mcommon.IsIntInSlice(withdrawIDs, sendRow.RelatedID) {
					withdrawIDs = append(withdrawIDs, sendRow.RelatedID)
				}
			case app.SendRelationTypeTxErc20:
				if !mcommon.IsIntInSlice(erc20TxIDs, sendRow.RelatedID) {
					erc20TxIDs = append(erc20TxIDs, sendRow.RelatedID)
				}
			case app.SendRelationTypeTxErc20Fee:
				if !mcommon.IsIntInSlice(erc20TxFeeIDs, sendRow.RelatedID) {
					erc20TxFeeIDs = append(erc20TxFeeIDs, sendRow.RelatedID)
				}
			}
			// 如果是提币，创建通知信息
			if sendRow.RelatedType == app.SendRelationTypeWithdraw {
				withdrawRow, ok := withdrawMap[sendRow.RelatedID]
				if !ok {
					mcommon.Log.Errorf("withdrawMap no: %d", sendRow.RelatedID)
					return nil
				}
				productRow, ok := productMap[withdrawRow.ProductID]
				if !ok {
					mcommon.Log.Errorf("productMap no: %d", withdrawRow.ProductID)
					return nil
				}
				nonce := mcommon.GetUUIDStr()
				reqObj := gin.H{
					"tx_hash":     sendRow.TxID,
					"balance":     withdrawRow.BalanceReal,
					"app_name":    productRow.AppName,
					"out_serial":  withdrawRow.OutSerial,
					"address":     withdrawRow.ToAddress,
					"symbol":      withdrawRow.Symbol,
					"notify_type": app.NotifyTypeWithdrawSend,
				}
				reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
				req, err := json.Marshal(reqObj)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return err
				}
				notifyRows = append(notifyRows, &model.DBTProductNotify{
					Nonce:        nonce,
					ProductID:    withdrawRow.ProductID,
					ItemType:     app.SendRelationTypeWithdraw,
					ItemID:       withdrawRow.ID,
					NotifyType:   app.NotifyTypeWithdrawSend,
					TokenSymbol:  withdrawRow.Symbol,
					URL:          productRow.CbURL,
					Msg:          string(req),
					HandleStatus: app.NotifyStatusInit,
					HandleMsg:    "",
					CreateTime:   now,
					UpdateTime:   now,
				})
			}
			return nil
		}
		for _, sendRow := range sendRows {
			// 发送数据中需要排除占位数据
			if sendRow.Hex != "" {
				rawTxBytes, err := hex.DecodeString(sendRow.Hex)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					continue
				}
				tx := new(types.Transaction)
				err = rlp.DecodeBytes(rawTxBytes, &tx)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					continue
				}
				err = ethclient.RPCSendTransaction(
					context.Background(),
					tx,
				)
				if err != nil {
					if !strings.Contains(err.Error(), "known transaction") {
						mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
						continue
					}
				}
				sendTxHashes = append(sendTxHashes, sendRow.TxID)

				err = onSendOk(sendRow)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
			} else if mcommon.IsStringInSlice(sendTxHashes, sendRow.TxID) {
				err = onSendOk(sendRow)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
			}
		}
		// 插入通知
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新提币状态
		_, err = app.SQLUpdateTWithdrawStatusByIDs(
			context.Background(),
			xenv.DbCon,
			withdrawIDs,
			&model.DBTWithdraw{
				HandleStatus: app.WithdrawStatusSend,
				HandleMsg:    "send",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新eth零钱整理状态
		_, err = app.SQLUpdateTTxOrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			txIDs,
			model.DBTTx{
				OrgStatus: app.TxOrgStatusSend,
				OrgMsg:    "send",
				OrgTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新erc20零钱整理状态
		_, err = app.SQLUpdateTTxErc20OrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			erc20TxIDs,
			model.DBTTxErc20{
				OrgStatus: app.TxOrgStatusSend,
				OrgMsg:    "send",
				OrgTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新erc20手续费状态
		_, err = app.SQLUpdateTTxErc20OrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			erc20TxFeeIDs,
			model.DBTTxErc20{
				OrgStatus: app.TxOrgStatusFeeSend,
				OrgMsg:    "fee send",
				OrgTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新发送状态
		_, err = app.SQLUpdateTSendStatusByIDs(
			context.Background(),
			xenv.DbCon,
			sendIDs,
			model.DBTSend{
				HandleStatus: app.SendStatusSend,
				HandleMsg:    "send",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}

// CheckRawTxConfirm 确认tx是否打包完成
func CheckRawTxConfirm() {
	lockKey := "EthCheckRawTxConfirm"
	app.LockWrap(lockKey, func() {
		sendRows, err := app.SQLSelectTSendColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTSendID,
				model.DBColTSendRelatedType,
				model.DBColTSendRelatedID,
				model.DBColTSendID,
				model.DBColTSendTxID,
			},
			app.SendStatusSend,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var withdrawIDs []int64
		for _, sendRow := range sendRows {
			if sendRow.RelatedType == app.SendRelationTypeWithdraw {
				// 提币
				if !mcommon.IsIntInSlice(withdrawIDs, sendRow.RelatedID) {
					withdrawIDs = append(withdrawIDs, sendRow.RelatedID)
				}
			}
		}
		withdrawMap, err := app.SQLGetWithdrawMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawProductID,
				model.DBColTWithdrawOutSerial,
				model.DBColTWithdrawToAddress,
				model.DBColTWithdrawBalanceReal,
				model.DBColTWithdrawSymbol,
			},
			withdrawIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var productIDs []int64
		for _, withdrawRow := range withdrawMap {
			if !mcommon.IsIntInSlice(productIDs, withdrawRow.ProductID) {
				productIDs = append(productIDs, withdrawRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		now := time.Now().Unix()
		var notifyRows []*model.DBTProductNotify
		var sendIDs []int64
		var txIDs []int64
		var erc20TxIDs []int64
		var erc20TxFeeIDs []int64
		withdrawIDs = []int64{}
		var sendHashes []string
		for _, sendRow := range sendRows {
			if !mcommon.IsStringInSlice(sendHashes, sendRow.TxID) {
				rpcTx, err := ethclient.RPCTransactionByHash(
					context.Background(),
					sendRow.TxID,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					continue
				}
				if rpcTx == nil {
					continue
				}
				sendHashes = append(sendHashes, sendRow.TxID)
			}
			if sendRow.RelatedType == app.SendRelationTypeWithdraw {
				// 提币
				withdrawRow, ok := withdrawMap[sendRow.RelatedID]
				if !ok {
					mcommon.Log.Errorf("no withdrawMap: %d", sendRow.RelatedID)
					return
				}
				productRow, ok := productMap[withdrawRow.ProductID]
				if !ok {
					mcommon.Log.Errorf("no productMap: %d", withdrawRow.ProductID)
					return
				}
				nonce := mcommon.GetUUIDStr()
				reqObj := gin.H{
					"tx_hash":     sendRow.TxID,
					"balance":     withdrawRow.BalanceReal,
					"app_name":    productRow.AppName,
					"out_serial":  withdrawRow.OutSerial,
					"address":     withdrawRow.ToAddress,
					"symbol":      withdrawRow.Symbol,
					"notify_type": app.NotifyTypeWithdrawConfirm,
				}
				reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
				req, err := json.Marshal(reqObj)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				notifyRows = append(notifyRows, &model.DBTProductNotify{
					Nonce:        nonce,
					ProductID:    withdrawRow.ProductID,
					ItemType:     app.SendRelationTypeWithdraw,
					ItemID:       withdrawRow.ID,
					NotifyType:   app.NotifyTypeWithdrawConfirm,
					TokenSymbol:  withdrawRow.Symbol,
					URL:          productRow.CbURL,
					Msg:          string(req),
					HandleStatus: app.NotifyStatusInit,
					HandleMsg:    "",
					CreateTime:   now,
					UpdateTime:   now,
				})

			}
			// 将发送成功和占位数据计入数组
			if !mcommon.IsIntInSlice(sendIDs, sendRow.ID) {
				sendIDs = append(sendIDs, sendRow.ID)
			}
			switch sendRow.RelatedType {
			case app.SendRelationTypeTx:
				if !mcommon.IsIntInSlice(txIDs, sendRow.RelatedID) {
					txIDs = append(txIDs, sendRow.RelatedID)
				}
			case app.SendRelationTypeWithdraw:
				if !mcommon.IsIntInSlice(withdrawIDs, sendRow.RelatedID) {
					withdrawIDs = append(withdrawIDs, sendRow.RelatedID)
				}
			case app.SendRelationTypeTxErc20:
				if !mcommon.IsIntInSlice(erc20TxIDs, sendRow.RelatedID) {
					erc20TxIDs = append(erc20TxIDs, sendRow.RelatedID)
				}
			case app.SendRelationTypeTxErc20Fee:
				if !mcommon.IsIntInSlice(erc20TxFeeIDs, sendRow.RelatedID) {
					erc20TxFeeIDs = append(erc20TxFeeIDs, sendRow.RelatedID)
				}
			}
		}
		// 添加通知信息
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新提币状态
		_, err = app.SQLUpdateTWithdrawStatusByIDs(
			context.Background(),
			xenv.DbCon,
			withdrawIDs,
			&model.DBTWithdraw{
				HandleStatus: app.WithdrawStatusConfirm,
				HandleMsg:    "confirmed",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新eth零钱整理状态
		_, err = app.SQLUpdateTTxOrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			txIDs,
			model.DBTTx{
				OrgStatus: app.TxOrgStatusConfirm,
				OrgMsg:    "confirmed",
				OrgTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新erc20零钱整理状态
		_, err = app.SQLUpdateTTxErc20OrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			erc20TxIDs,
			model.DBTTxErc20{
				OrgStatus: app.TxOrgStatusConfirm,
				OrgMsg:    "confirmed",
				OrgTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新erc20零钱整理eth手续费状态
		_, err = app.SQLUpdateTTxErc20OrgStatusByIDs(
			context.Background(),
			xenv.DbCon,
			erc20TxFeeIDs,
			model.DBTTxErc20{
				OrgStatus: app.TxOrgStatusFeeConfirm,
				OrgMsg:    "eth fee confirmed",
				OrgTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 更新发送状态
		_, err = app.SQLUpdateTSendStatusByIDs(
			context.Background(),
			xenv.DbCon,
			sendIDs,
			model.DBTSend{
				HandleStatus: app.SendStatusConfirm,
				HandleMsg:    "confirmed",
				HandleTime:   now,
			},
		)
	})
}

// CheckWithdraw 检测提现
func CheckWithdraw() {
	lockKey := "EthCheckWithdraw"
	app.LockWrap(lockKey, func() {
		// 获取需要处理的提币数据
		withdrawRows, err := app.SQLSelectTWithdrawColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTWithdrawID,
			},
			app.WithdrawStatusInit,
			[]string{CoinSymbol},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(withdrawRows) == 0 {
			// 没有要处理的提币
			return
		}
		// 获取热钱包地址
		hotAddressValue, err := app.SQLGetTAppConfigStrValueByK(
			context.Background(),
			xenv.DbCon,
			"hot_wallet_address_eth",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = StrToAddressBytes(hotAddressValue)
		if err != nil {
			mcommon.Log.Errorf("eth hot address err: [%T] %s", err, err.Error())
			return
		}
		// 获取私钥
		privateKey, err := GetPkOfAddress(
			context.Background(),
			xenv.DbCon,
			hotAddressValue,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取热钱包余额
		hotAddressBalance, err := ethclient.RPCBalanceAt(
			context.Background(),
			hotAddressValue,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		pendingBalanceRealStr, err := app.SQLGetTSendPendingBalanceReal(
			context.Background(),
			xenv.DbCon,
			hotAddressValue,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		pendingBalance, err := EthStrToWeiBigInit(pendingBalanceRealStr)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		hotAddressBalance.Sub(hotAddressBalance, pendingBalance)
		// 获取gap price
		gasPriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"to_user_gas_price_eth",
		)
		if err != nil {
			mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
			return
		}
		gasPrice := gasPriceValue
		gasLimit := int64(21000)
		feeValue := gasLimit * gasPrice
		chainID, err := ethclient.RPCNetworkID(context.Background())
		if err != nil {
			mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
			return
		}
		for _, withdrawRow := range withdrawRows {
			err = handleWithdraw(withdrawRow.ID, chainID, hotAddressValue, privateKey, hotAddressBalance, gasLimit, gasPrice, feeValue)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
		}
	})
}

func handleWithdraw(withdrawID int64, chainID int64, hotAddress string, privateKey *ecdsa.PrivateKey, hotAddressBalance *big.Int, gasLimit, gasPrice, feeValue int64) error {
	isComment := false
	dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if !isComment {
			_ = dbTx.Rollback()
		}
	}()
	// 处理业务
	withdrawRow, err := app.SQLGetTWithdrawColForUpdate(
		context.Background(),
		dbTx,
		[]string{
			model.DBColTWithdrawID,
			model.DBColTWithdrawBalanceReal,
			model.DBColTWithdrawToAddress,
		},
		withdrawID,
		app.WithdrawStatusInit,
	)
	if err != nil {
		return err
	}
	if withdrawRow == nil {
		return nil
	}
	balanceBigInt, err := EthStrToWeiBigInit(withdrawRow.BalanceReal)
	if err != nil {
		return err
	}
	hotAddressBalance.Sub(hotAddressBalance, balanceBigInt)
	hotAddressBalance.Sub(hotAddressBalance, big.NewInt(feeValue))
	if hotAddressBalance.Cmp(new(big.Int)) < 0 {
		mcommon.Log.Errorf("hot balance limit")
		hotAddressBalance.Add(hotAddressBalance, balanceBigInt)
		hotAddressBalance.Add(hotAddressBalance, big.NewInt(feeValue))
		return nil
	}
	// nonce
	nonce, err := GetNonce(
		dbTx,
		hotAddress,
	)
	if err != nil {
		return err
	}
	// 创建交易
	var data []byte
	toAddress, err := StrToAddressBytes(withdrawRow.ToAddress)
	if err != nil {
		return err
	}
	tx := types.NewTransaction(
		uint64(nonce),
		toAddress,
		balanceBigInt,
		uint64(gasLimit),
		big.NewInt(gasPrice),
		data,
	)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainID)), privateKey)
	if err != nil {
		return err
	}
	ts := types.Transactions{signedTx}
	rawTxBytes := ts.GetRlp(0)
	rawTxHex := hex.EncodeToString(rawTxBytes)
	txHash := strings.ToLower(signedTx.Hash().Hex())
	now := time.Now().Unix()
	_, err = app.SQLUpdateTWithdrawGenTx(
		context.Background(),
		dbTx,
		&model.DBTWithdraw{
			ID:           withdrawID,
			TxHash:       txHash,
			HandleStatus: app.WithdrawStatusHex,
			HandleMsg:    "hex",
			HandleTime:   now,
		},
	)
	if err != nil {
		return err
	}
	_, err = model.SQLCreateTSend(
		context.Background(),
		dbTx,
		&model.DBTSend{
			RelatedType:  app.SendRelationTypeWithdraw,
			RelatedID:    withdrawID,
			TxID:         txHash,
			FromAddress:  hotAddress,
			ToAddress:    withdrawRow.ToAddress,
			BalanceReal:  withdrawRow.BalanceReal,
			Gas:          gasLimit,
			GasPrice:     gasPrice,
			Nonce:        nonce,
			Hex:          rawTxHex,
			HandleStatus: app.SendStatusInit,
			HandleMsg:    "",
			HandleTime:   now,
		},
		false,
	)
	if err != nil {
		return err
	}
	// 处理完成
	err = dbTx.Commit()
	if err != nil {
		return err
	}
	isComment = true
	return nil
}

// CheckTxNotify 创建eth冲币通知
func CheckTxNotify() {
	lockKey := "EthCheckTxNotify"
	app.LockWrap(lockKey, func() {
		txRows, err := app.SQLSelectTTxColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTTxID,
				model.DBColTTxProductID,
				model.DBColTTxTxID,
				model.DBColTTxToAddress,
				model.DBColTTxBalanceReal,
			},
			app.TxStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var productIDs []int64
		for _, txRow := range txRows {
			if !mcommon.IsIntInSlice(productIDs, txRow.ProductID) {
				productIDs = append(productIDs, txRow.ProductID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		var notifyTxIDs []int64
		var notifyRows []*model.DBTProductNotify
		now := time.Now().Unix()
		for _, txRow := range txRows {
			productRow, ok := productMap[txRow.ProductID]
			if !ok {
				mcommon.Log.Warnf("no productMap: %d", txRow.ProductID)
				notifyTxIDs = append(notifyTxIDs, txRow.ID)
				continue
			}
			nonce := mcommon.GetUUIDStr()
			reqObj := gin.H{
				"tx_hash":     txRow.TxID,
				"app_name":    productRow.AppName,
				"address":     txRow.ToAddress,
				"balance":     txRow.BalanceReal,
				"symbol":      CoinSymbol,
				"notify_type": app.NotifyTypeTx,
			}
			reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
			req, err := json.Marshal(reqObj)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
			notifyRows = append(notifyRows, &model.DBTProductNotify{
				Nonce:        nonce,
				ProductID:    txRow.ProductID,
				ItemType:     app.SendRelationTypeTx,
				ItemID:       txRow.ID,
				NotifyType:   app.NotifyTypeTx,
				TokenSymbol:  CoinSymbol,
				URL:          productRow.CbURL,
				Msg:          string(req),
				HandleStatus: app.NotifyStatusInit,
				HandleMsg:    "",
				CreateTime:   now,
				UpdateTime:   now,
			})
			notifyTxIDs = append(notifyTxIDs, txRow.ID)
		}
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = app.SQLUpdateTTxStatusByIDs(
			context.Background(),
			xenv.DbCon,
			notifyTxIDs,
			model.DBTTx{
				HandleStatus: app.TxStatusNotify,
				HandleMsg:    "notify",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}

// CheckErc20BlockSeek 检测erc20到账
func CheckErc20BlockSeek() {
	lockKey := "Erc20CheckBlockSeek"
	app.LockWrap(lockKey, func() {
		// 获取配置 延迟确认数
		confirmValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"block_confirm_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 获取状态 当前处理完成的最新的block number
		seekValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"erc20_seek_num",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// rpc 获取当前最新区块数
		rpcBlockNum, err := ethclient.RPCBlockNumber(context.Background())
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		startI := seekValue + 1
		endI := rpcBlockNum - confirmValue + 1
		if startI < endI {
			// 读取abi
			type LogTransfer struct {
				From   string
				To     string
				Tokens *big.Int
			}
			contractAbi, err := abi.JSON(strings.NewReader(ethclient.EthABI))
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 获取所有token
			var configTokenRowAddresses []string
			configTokenRowMap := make(map[string]*model.DBTAppConfigToken)
			configTokenRows, err := app.SQLSelectTAppConfigTokenColAll(
				context.Background(),
				xenv.DbCon,
				[]string{
					model.DBColTAppConfigTokenID,
					model.DBColTAppConfigTokenTokenAddress,
					model.DBColTAppConfigTokenTokenDecimals,
					model.DBColTAppConfigTokenTokenSymbol,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			for _, contractRow := range configTokenRows {
				configTokenRowAddresses = append(configTokenRowAddresses, contractRow.TokenAddress)
				configTokenRowMap[strings.ToLower(contractRow.TokenAddress)] = contractRow
			}
			// 遍历获取需要查询的block信息
			for i := startI; i < endI; i++ {
				//mcommon.Log.Debugf("erc20 check block: %d", i)
				if len(configTokenRowAddresses) > 0 {
					// rpc获取block信息
					logs, err := ethclient.RPCFilterLogs(
						context.Background(),
						i,
						i,
						configTokenRowAddresses,
						contractAbi.Events["Transfer"],
					)
					if err != nil {
						mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
						return
					}
					// 接收地址列表
					var toAddresses []string
					// map[接收地址] => []交易信息
					toAddressLogMap := make(map[string][]types.Log)
					for _, log := range logs {
						if log.Removed {
							continue
						}
						toAddress := AddressBytesToStr(common.HexToAddress(log.Topics[2].Hex()))
						if !mcommon.IsStringInSlice(toAddresses, toAddress) {
							toAddresses = append(toAddresses, toAddress)
						}
						toAddressLogMap[toAddress] = append(toAddressLogMap[toAddress], log)
					}
					// 从db中查询这些地址是否是冲币地址中的地址
					dbAddressRows, err := app.SQLSelectTAddressKeyColByAddress(
						context.Background(),
						xenv.DbCon,
						[]string{
							model.DBColTAddressKeyAddress,
							model.DBColTAddressKeyUseTag,
						},
						toAddresses,
					)
					if err != nil {
						mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
						return
					}
					// map[接收地址] => 产品id
					addressProductMap := make(map[string]int64)
					for _, dbAddressRow := range dbAddressRows {
						addressProductMap[dbAddressRow.Address] = dbAddressRow.UseTag
					}
					// 时间
					now := time.Now().Unix()
					// 待添加数组
					var txErc20Rows []*model.DBTTxErc20
					// 遍历数据库中有交易的地址
					for _, dbAddressRow := range dbAddressRows {
						if dbAddressRow.UseTag < 0 {
							continue
						}
						// 获取地址对应的交易列表
						logs, ok := toAddressLogMap[dbAddressRow.Address]
						if !ok {
							mcommon.Log.Errorf("toAddressLogMap no: %s", dbAddressRow.Address)
							return
						}
						for _, log := range logs {
							var transferEvent LogTransfer
							err := contractAbi.UnpackIntoInterface(&transferEvent, "Transfer", log.Data)
							if err != nil {
								mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
								return
							}
							transferEvent.From = strings.ToLower(common.HexToAddress(log.Topics[1].Hex()).Hex())
							transferEvent.To = strings.ToLower(common.HexToAddress(log.Topics[2].Hex()).Hex())
							contractAddress := strings.ToLower(log.Address.Hex())
							configTokenRow, ok := configTokenRowMap[contractAddress]
							if !ok {
								mcommon.Log.Errorf("no configTokenRowMap of: %s", contractAddress)
								return
							}
							rpcTxReceipt, err := ethclient.RPCTransactionReceipt(
								context.Background(),
								log.TxHash.Hex(),
							)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							if rpcTxReceipt.Status <= 0 {
								continue
							}
							rpcTx, err := ethclient.RPCTransactionByHash(
								context.Background(),
								log.TxHash.Hex(),
							)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							if strings.ToLower(rpcTx.To().Hex()) != contractAddress {
								// 合约地址和tx的to地址不匹配
								continue
							}
							// 检测input
							input, err := contractAbi.Pack(
								"transfer",
								common.HexToAddress(log.Topics[2].Hex()),
								transferEvent.Tokens,
							)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							if hexutil.Encode(input) != hexutil.Encode(rpcTx.Data()) {
								// input 不匹配
								continue
							}
							balanceReal, err := TokenWeiBigIntToEthStr(transferEvent.Tokens, configTokenRow.TokenDecimals)
							if err != nil {
								mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
								return
							}
							// 放入待插入数组
							txErc20Rows = append(txErc20Rows, &model.DBTTxErc20{
								TokenID:      configTokenRow.ID,
								ProductID:    addressProductMap[transferEvent.To],
								TxID:         log.TxHash.Hex(),
								FromAddress:  transferEvent.From,
								ToAddress:    transferEvent.To,
								BalanceReal:  balanceReal,
								CreateTime:   now,
								HandleStatus: app.TxStatusInit,
								HandleMsg:    "",
								HandleTime:   now,
								OrgStatus:    app.TxOrgStatusInit,
								OrgMsg:       "",
								OrgTime:      now,
							})
						}
					}
					_, err = model.SQLCreateManyTTxErc20(
						context.Background(),
						xenv.DbCon,
						txErc20Rows,
						true,
					)
					if err != nil {
						mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
						return
					}
				}
				// 更新检查到的最新区块数
				_, err = app.SQLUpdateTAppStatusIntByKGreater(
					context.Background(),
					xenv.DbCon,
					&model.DBTAppStatusInt{
						K: "erc20_seek_num",
						V: i,
					},
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
			}
		}
	})
}

// CheckErc20TxNotify 创建erc20冲币通知
func CheckErc20TxNotify() {
	lockKey := "Erc20CheckTxNotify"
	app.LockWrap(lockKey, func() {
		txRows, err := app.SQLSelectTTxErc20ColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTTxErc20ID,
				model.DBColTTxErc20TokenID,
				model.DBColTTxErc20ProductID,
				model.DBColTTxErc20TxID,
				model.DBColTTxErc20ToAddress,
				model.DBColTTxErc20BalanceReal,
			},
			app.TxStatusInit,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		var productIDs []int64
		var tokenIDs []int64
		for _, txRow := range txRows {
			if !mcommon.IsIntInSlice(productIDs, txRow.ProductID) {
				productIDs = append(productIDs, txRow.ProductID)
			}
			if !mcommon.IsIntInSlice(tokenIDs, txRow.TokenID) {
				tokenIDs = append(tokenIDs, txRow.TokenID)
			}
		}
		productMap, err := app.SQLGetProductMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTProductID,
				model.DBColTProductAppName,
				model.DBColTProductCbURL,
				model.DBColTProductAppSk,
			},
			productIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		tokenMap, err := app.SQLGetAppConfigTokenMap(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTAppConfigTokenID,
				model.DBColTAppConfigTokenTokenSymbol,
			},
			tokenIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		var notifyTxIDs []int64
		var notifyRows []*model.DBTProductNotify
		now := time.Now().Unix()
		for _, txRow := range txRows {
			productRow, ok := productMap[txRow.ProductID]
			if !ok {
				mcommon.Log.Warnf("productMap no: %d", txRow.ProductID)
				notifyTxIDs = append(notifyTxIDs, txRow.ID)
				continue
			}
			tokenRow, ok := tokenMap[txRow.TokenID]
			if !ok {
				mcommon.Log.Errorf("tokenMap no: %d", txRow.TokenID)
				continue
			}
			nonce := mcommon.GetUUIDStr()
			reqObj := gin.H{
				"tx_hash":     txRow.TxID,
				"app_name":    productRow.AppName,
				"address":     txRow.ToAddress,
				"balance":     txRow.BalanceReal,
				"symbol":      tokenRow.TokenSymbol,
				"notify_type": app.NotifyTypeTx,
			}
			reqObj["sign"] = mcommon.WechatGetSign(productRow.AppSk, reqObj)
			req, err := json.Marshal(reqObj)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			notifyRows = append(notifyRows, &model.DBTProductNotify{
				Nonce:        nonce,
				ProductID:    txRow.ProductID,
				ItemType:     app.SendRelationTypeTx,
				ItemID:       txRow.ID,
				NotifyType:   app.NotifyTypeTx,
				TokenSymbol:  tokenRow.TokenSymbol,
				URL:          productRow.CbURL,
				Msg:          string(req),
				HandleStatus: app.NotifyStatusInit,
				HandleMsg:    "",
				CreateTime:   now,
				UpdateTime:   now,
			})
			notifyTxIDs = append(notifyTxIDs, txRow.ID)
		}
		_, err = model.SQLCreateManyTProductNotify(
			context.Background(),
			xenv.DbCon,
			notifyRows,
			true,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = app.SQLUpdateTTxErc20StatusByIDs(
			context.Background(),
			xenv.DbCon,
			notifyTxIDs,
			model.DBTTxErc20{
				HandleStatus: app.TxStatusNotify,
				HandleMsg:    "notify",
				HandleTime:   now,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}

// CheckErc20TxOrg erc20零钱整理
func CheckErc20TxOrg() {
	lockKey := "Erc20CheckTxOrg"
	app.LockWrap(lockKey, func() {
		// 计算转账token所需的手续费
		erc20GasUseValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"erc20_gas_use",
		)
		if err != nil {
			mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
			return
		}
		gasPriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"to_cold_gas_price_eth",
		)
		if err != nil {
			mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
			return
		}
		erc20Fee := big.NewInt(erc20GasUseValue * gasPriceValue)
		ethGasUse := int64(21000)
		ethFee := big.NewInt(ethGasUse * gasPriceValue)
		// chainID
		chainID, err := ethclient.RPCNetworkID(context.Background())
		if err != nil {
			mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
			return
		}

		// 开始事物
		isComment := false
		dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
		if err != nil {
			mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
			return
		}
		defer func() {
			if !isComment {
				_ = dbTx.Rollback()
			}
		}()
		// 查询需要处理的交易
		txRows, err := app.SQLSelectTTxErc20ColByOrgForUpdate(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTTxErc20ID,
				model.DBColTTxErc20TokenID,
				model.DBColTTxErc20ProductID,
				model.DBColTTxErc20ToAddress,
				model.DBColTTxErc20BalanceReal,
			},
			[]int64{app.TxOrgStatusInit, app.TxOrgStatusFeeConfirm},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(txRows) <= 0 {
			return
		}
		// 整理信息
		type StOrgInfo struct {
			TxIDs        []int64
			ToAddress    string
			TokenID      int64
			TokenBalance *big.Int
		}

		var tokenIDs []int64
		for _, txRow := range txRows {
			if !mcommon.IsIntInSlice(tokenIDs, txRow.TokenID) {
				tokenIDs = append(tokenIDs, txRow.TokenID)
			}
		}
		tokenMap, err := app.SQLGetAppConfigTokenMap(
			context.Background(),
			dbTx,
			[]string{
				model.DBColTAppConfigTokenID,
				model.DBColTAppConfigTokenTokenAddress,
				model.DBColTAppConfigTokenTokenDecimals,
				model.DBColTAppConfigTokenTokenSymbol,
				model.DBColTAppConfigTokenColdAddress,
				model.DBColTAppConfigTokenOrgMinBalance,
			},
			tokenIDs,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}

		txMap := make(map[int64]*model.DBTTxErc20)
		// 地址eth余额
		addressEthBalanceMap := make(map[string]*big.Int)
		// 整理信息map
		orgMap := make(map[string]*StOrgInfo)
		// 整理地址
		var toAddresses []string
		for _, txRow := range txRows {
			tokenRow, ok := tokenMap[txRow.TokenID]
			if !ok {
				mcommon.Log.Errorf("no token of: %d", txRow.TokenID)
				return
			}
			// 转换为map
			txMap[txRow.ID] = txRow
			// 读取eth余额
			_, ok = addressEthBalanceMap[txRow.ToAddress]
			if !ok {
				balance, err := ethclient.RPCBalanceAt(
					context.Background(),
					txRow.ToAddress,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				addressEthBalanceMap[txRow.ToAddress] = balance
			}
			// 整理信息
			orgKey := fmt.Sprintf("%s-%d", txRow.ToAddress, txRow.TokenID)
			orgInfo, ok := orgMap[orgKey]
			if !ok {
				orgInfo = &StOrgInfo{
					TokenID:      txRow.TokenID,
					ToAddress:    txRow.ToAddress,
					TokenBalance: new(big.Int),
				}
				orgMap[orgKey] = orgInfo
			}
			orgInfo.TxIDs = append(orgInfo.TxIDs, txRow.ID)
			txBalance, err := TokenEthStrToWeiBigInit(txRow.BalanceReal, tokenRow.TokenDecimals)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			orgInfo.TokenBalance.Add(orgInfo.TokenBalance, txBalance)
			// 待查询id
			if !mcommon.IsStringInSlice(toAddresses, txRow.ToAddress) {
				toAddresses = append(toAddresses, txRow.ToAddress)
			}
		}
		// 整理地址key
		addressPKMap, err := GetPKMapOfAddresses(
			context.Background(),
			dbTx,
			toAddresses,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		// 需要手续费的整理信息
		now := time.Now().Unix()
		needEthFeeMap := make(map[string]*StOrgInfo)
		for k, orgInfo := range orgMap {
			toAddress := orgInfo.ToAddress
			// 计算eth费用
			addressEthBalanceMap[toAddress] = addressEthBalanceMap[toAddress].Sub(addressEthBalanceMap[toAddress], erc20Fee)
			if addressEthBalanceMap[toAddress].Cmp(new(big.Int)) < 0 {
				// eth手续费不足
				// 处理添加手续费
				needEthFeeMap[k] = orgInfo
				continue
			}
			tokenRow, ok := tokenMap[orgInfo.TokenID]
			if !ok {
				mcommon.Log.Errorf("no tokenMap: %d", orgInfo.TokenID)
				continue
			}

			orgMinBalance, err := TokenEthStrToWeiBigInit(tokenRow.OrgMinBalance, tokenRow.TokenDecimals)
			if err != nil {
				mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
				continue
			}
			if orgInfo.TokenBalance.Cmp(orgMinBalance) < 0 {
				mcommon.Log.Errorf("token balance < org min balance")
				continue
			}
			// 处理token转账
			privateKey, ok := addressPKMap[toAddress]
			if !ok {
				mcommon.Log.Errorf("addressMap no: %s", toAddress)
				continue
			}
			// 获取nonce值
			nonce, err := GetNonce(dbTx, toAddress)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
			// 生成交易
			contractAbi, err := abi.JSON(strings.NewReader(ethclient.EthABI))
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			input, err := contractAbi.Pack(
				"transfer",
				common.HexToAddress(tokenRow.ColdAddress),
				orgInfo.TokenBalance,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			rpcTx := types.NewTransaction(
				uint64(nonce),
				common.HexToAddress(tokenRow.TokenAddress),
				big.NewInt(0),
				uint64(erc20GasUseValue),
				big.NewInt(gasPriceValue),
				input,
			)
			signedTx, err := types.SignTx(rpcTx, types.NewEIP155Signer(big.NewInt(chainID)), privateKey)
			if err != nil {
				mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
				continue
			}
			ts := types.Transactions{signedTx}
			rawTxBytes := ts.GetRlp(0)
			rawTxHex := hex.EncodeToString(rawTxBytes)
			txHash := strings.ToLower(signedTx.Hash().Hex())
			// 创建存入数据
			balanceReal, err := TokenWeiBigIntToEthStr(orgInfo.TokenBalance, tokenRow.TokenDecimals)
			if err != nil {
				mcommon.Log.Warnf("err: [%T] %s", err, err.Error())
				continue
			}
			// 待插入数据
			var sendRows []*model.DBTSend
			for rowIndex, txID := range orgInfo.TxIDs {
				if rowIndex == 0 {
					sendRows = append(sendRows, &model.DBTSend{
						RelatedType:  app.SendRelationTypeTxErc20,
						RelatedID:    txID,
						TokenID:      orgInfo.TokenID,
						TxID:         txHash,
						FromAddress:  toAddress,
						ToAddress:    tokenRow.ColdAddress,
						BalanceReal:  balanceReal,
						Gas:          erc20GasUseValue,
						GasPrice:     gasPriceValue,
						Nonce:        nonce,
						Hex:          rawTxHex,
						CreateTime:   now,
						HandleStatus: app.SendStatusInit,
						HandleMsg:    "",
						HandleTime:   now,
					})
				} else {
					sendRows = append(sendRows, &model.DBTSend{
						RelatedType:  app.SendRelationTypeTxErc20,
						RelatedID:    txID,
						TokenID:      orgInfo.TokenID,
						TxID:         txHash,
						FromAddress:  toAddress,
						ToAddress:    tokenRow.ColdAddress,
						BalanceReal:  "",
						Gas:          0,
						GasPrice:     0,
						Nonce:        -1,
						Hex:          "",
						CreateTime:   now,
						HandleStatus: app.SendStatusInit,
						HandleMsg:    "",
						HandleTime:   now,
					})
				}
			}
			// 插入发送队列
			_, err = model.SQLCreateManyTSend(
				context.Background(),
				dbTx,
				sendRows,
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 更新整理状态
			_, err = app.SQLUpdateTTxErc20OrgStatusByIDs(
				context.Background(),
				dbTx,
				orgInfo.TxIDs,
				model.DBTTxErc20{
					OrgStatus: app.TxOrgStatusHex,
					OrgMsg:    "hex",
					OrgTime:   now,
				},
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		// 生成eth转账
		if len(needEthFeeMap) > 0 {
			// 获取热钱包地址
			feeAddressValue, err := app.SQLGetTAppConfigStrValueByK(
				context.Background(),
				dbTx,
				"fee_wallet_address_erc20",
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			_, err = StrToAddressBytes(feeAddressValue)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			// 获取私钥
			privateKey, err := GetPkOfAddress(
				context.Background(),
				dbTx,
				feeAddressValue,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			feeAddressBalance, err := ethclient.RPCBalanceAt(
				context.Background(),
				feeAddressValue,
			)
			if err != nil {
				mcommon.Log.Errorf("RPCBalanceAt err: [%T] %s", err, err.Error())
				return
			}
			pendingBalanceReal, err := app.SQLGetTSendPendingBalanceReal(
				context.Background(),
				dbTx,
				feeAddressValue,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			pendingBalance, err := EthStrToWeiBigInit(pendingBalanceReal)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			feeAddressBalance.Sub(feeAddressBalance, pendingBalance)
			// 生成手续费交易
			for _, orgInfo := range needEthFeeMap {
				feeAddressBalance.Sub(feeAddressBalance, ethFee)
				feeAddressBalance.Sub(feeAddressBalance, erc20Fee)
				if feeAddressBalance.Cmp(new(big.Int)) < 0 {
					mcommon.Log.Errorf("eth fee balance limit")
					return
				}
				// nonce
				nonce, err := GetNonce(
					dbTx,
					feeAddressValue,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 创建交易
				var data []byte
				tx := types.NewTransaction(
					uint64(nonce),
					common.HexToAddress(orgInfo.ToAddress),
					erc20Fee,
					uint64(ethGasUse),
					big.NewInt(gasPriceValue),
					data,
				)
				signedTx, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(chainID)), privateKey)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				ts := types.Transactions{signedTx}
				rawTxBytes := ts.GetRlp(0)
				rawTxHex := hex.EncodeToString(rawTxBytes)
				txHash := strings.ToLower(signedTx.Hash().Hex())
				now := time.Now().Unix()
				balanceReal, err := WeiBigIntToEthStr(erc20Fee)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 待插入数据
				var sendRows []*model.DBTSend
				for rowIndex, txID := range orgInfo.TxIDs {
					if rowIndex == 0 {
						sendRows = append(sendRows, &model.DBTSend{
							RelatedType:  app.SendRelationTypeTxErc20Fee,
							RelatedID:    txID,
							TokenID:      0,
							TxID:         txHash,
							FromAddress:  feeAddressValue,
							ToAddress:    orgInfo.ToAddress,
							BalanceReal:  balanceReal,
							Gas:          ethGasUse,
							GasPrice:     gasPriceValue,
							Nonce:        nonce,
							Hex:          rawTxHex,
							CreateTime:   now,
							HandleStatus: app.SendStatusInit,
							HandleMsg:    "",
							HandleTime:   now,
						})
					} else {
						sendRows = append(sendRows, &model.DBTSend{
							RelatedType:  app.SendRelationTypeTxErc20Fee,
							RelatedID:    txID,
							TokenID:      0,
							TxID:         txHash,
							FromAddress:  feeAddressValue,
							ToAddress:    orgInfo.ToAddress,
							BalanceReal:  "",
							Gas:          0,
							GasPrice:     0,
							Nonce:        -1,
							Hex:          "",
							CreateTime:   now,
							HandleStatus: app.SendStatusInit,
							HandleMsg:    "",
							HandleTime:   now,
						})
					}
				}
				// 插入发送数据
				_, err = model.SQLCreateManyTSend(
					context.Background(),
					dbTx,
					sendRows,
					true,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				// 更新整理状态
				_, err = app.SQLUpdateTTxErc20OrgStatusByIDs(
					context.Background(),
					dbTx,
					orgInfo.TxIDs,
					model.DBTTxErc20{
						OrgStatus: app.TxOrgStatusFeeHex,
						OrgMsg:    "fee hex",
						OrgTime:   now,
					},
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
			}
		}

		err = dbTx.Commit()
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		isComment = true
	})
}

// CheckErc20Withdraw erc20提币
func CheckErc20Withdraw() {
	lockKey := "Erc20CheckWithdraw"
	app.LockWrap(lockKey, func() {
		var tokenSymbols []string
		tokenMap := make(map[string]*model.DBTAppConfigToken)
		addressKeyMap := make(map[string]*ecdsa.PrivateKey)
		addressEthBalanceMap := make(map[string]*big.Int)
		addressTokenBalanceMap := make(map[string]*big.Int)
		tokenRows, err := app.SQLSelectTAppConfigTokenColAll(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTAppConfigTokenID,
				model.DBColTAppConfigTokenTokenAddress,
				model.DBColTAppConfigTokenTokenDecimals,
				model.DBColTAppConfigTokenTokenSymbol,
				model.DBColTAppConfigTokenHotAddress,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		for _, tokenRow := range tokenRows {
			tokenMap[tokenRow.TokenSymbol] = tokenRow
			if !mcommon.IsStringInSlice(tokenSymbols, tokenRow.TokenSymbol) {
				tokenSymbols = append(tokenSymbols, tokenRow.TokenSymbol)
			}
		}
		withdrawRows, err := app.SQLSelectTWithdrawColByStatus(
			context.Background(),
			xenv.DbCon,
			[]string{
				model.DBColTWithdrawID,
				model.DBColTWithdrawSymbol,
			},
			app.WithdrawStatusInit,
			tokenSymbols,
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		if len(withdrawRows) == 0 {
			return
		}
		for _, tokenRow := range tokenRows {
			// 获取私钥
			_, err = StrToAddressBytes(tokenRow.HotAddress)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
			hotAddress := tokenRow.HotAddress
			_, ok := addressKeyMap[hotAddress]
			if !ok {
				// 获取私钥
				keyRow, err := app.SQLGetTAddressKeyColByAddress(
					context.Background(),
					xenv.DbCon,
					[]string{
						model.DBColTAddressKeyPwd,
					},
					hotAddress,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				if keyRow == nil {
					mcommon.Log.Errorf("no key of: %s", hotAddress)
					return
				}
				key, err := mcommon.AesDecrypt(keyRow.Pwd, xenv.Cfg.AESKey)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				if len(key) == 0 {
					mcommon.Log.Errorf("error key of: %s", hotAddress)
					return
				}
				if strings.HasPrefix(key, "0x") {
					key = key[2:]
				}
				privateKey, err := crypto.HexToECDSA(key)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				addressKeyMap[hotAddress] = privateKey
			}
			_, ok = addressEthBalanceMap[hotAddress]
			if !ok {
				hotAddressBalance, err := ethclient.RPCBalanceAt(
					context.Background(),
					hotAddress,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				pendingBalanceReal, err := app.SQLGetTSendPendingBalanceReal(
					context.Background(),
					xenv.DbCon,
					hotAddress,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				pendingBalance, err := EthStrToWeiBigInit(pendingBalanceReal)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				hotAddressBalance.Sub(hotAddressBalance, pendingBalance)
				addressEthBalanceMap[hotAddress] = hotAddressBalance
			}
			tokenBalanceKey := fmt.Sprintf("%s-%s", tokenRow.HotAddress, tokenRow.TokenSymbol)
			_, ok = addressTokenBalanceMap[tokenBalanceKey]
			if !ok {
				tokenBalance, err := ethclient.RPCTokenBalance(
					context.Background(),
					tokenRow.TokenAddress,
					tokenRow.HotAddress,
				)
				if err != nil {
					mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
					return
				}
				addressTokenBalanceMap[tokenBalanceKey] = tokenBalance
			}
		}
		// 获取gap price
		gasPriceValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"to_user_gas_price_eth",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		gasPrice := gasPriceValue
		erc20GasUseValue, err := app.SQLGetTAppConfigIntValueByK(
			context.Background(),
			xenv.DbCon,
			"erc20_gas_use",
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		gasLimit := erc20GasUseValue
		// eth fee
		feeValue := big.NewInt(gasLimit * gasPrice)
		chainID, err := ethclient.RPCNetworkID(context.Background())
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		for _, withdrawRow := range withdrawRows {
			err = handleErc20Withdraw(withdrawRow.ID, chainID, &tokenMap, &addressKeyMap, &addressEthBalanceMap, &addressTokenBalanceMap, gasLimit, gasPrice, feeValue)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				continue
			}
		}
	})
}

func handleErc20Withdraw(withdrawID int64, chainID int64, tokenMap *map[string]*model.DBTAppConfigToken, addressKeyMap *map[string]*ecdsa.PrivateKey, addressEthBalanceMap *map[string]*big.Int, addressTokenBalanceMap *map[string]*big.Int, gasLimit, gasPrice int64, feeValue *big.Int) error {
	isComment := false
	dbTx, err := xenv.DbCon.BeginTxx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if !isComment {
			_ = dbTx.Rollback()
		}
	}()
	// 处理业务
	withdrawRow, err := app.SQLGetTWithdrawColForUpdate(
		context.Background(),
		dbTx,
		[]string{
			model.DBColTWithdrawID,
			model.DBColTWithdrawBalanceReal,
			model.DBColTWithdrawToAddress,
			model.DBColTWithdrawSymbol,
		},
		withdrawID,
		app.WithdrawStatusInit,
	)
	if err != nil {
		return err
	}
	if withdrawRow == nil {
		return nil
	}
	tokenRow, ok := (*tokenMap)[withdrawRow.Symbol]
	if !ok {
		mcommon.Log.Errorf("no tokenMap: %s", withdrawRow.Symbol)
		return nil
	}
	hotAddress := tokenRow.HotAddress
	key, ok := (*addressKeyMap)[hotAddress]
	if !ok {
		mcommon.Log.Errorf("no addressKeyMap: %s", hotAddress)
		return nil
	}
	(*addressEthBalanceMap)[hotAddress] = (*addressEthBalanceMap)[hotAddress].Sub(
		(*addressEthBalanceMap)[hotAddress],
		feeValue,
	)
	if (*addressEthBalanceMap)[hotAddress].Cmp(new(big.Int)) < 0 {
		mcommon.Log.Errorf("%s eth limit", hotAddress)
		return nil
	}
	tokenBalanceKey := fmt.Sprintf("%s-%s", tokenRow.HotAddress, tokenRow.TokenSymbol)
	tokenBalance, err := TokenEthStrToWeiBigInit(withdrawRow.BalanceReal, tokenRow.TokenDecimals)
	if err != nil {
		return err
	}
	(*addressTokenBalanceMap)[tokenBalanceKey] = (*addressTokenBalanceMap)[tokenBalanceKey].Sub(
		(*addressTokenBalanceMap)[tokenBalanceKey],
		tokenBalance,
	)
	if (*addressTokenBalanceMap)[tokenBalanceKey].Cmp(new(big.Int)) < 0 {
		mcommon.Log.Errorf("%s token limit", tokenBalanceKey)
		return nil
	}
	// 获取nonce值
	nonce, err := GetNonce(dbTx, hotAddress)
	if err != nil {
		return err
	}
	// 生成交易
	contractAbi, err := abi.JSON(strings.NewReader(ethclient.EthABI))
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return err
	}
	input, err := contractAbi.Pack(
		"transfer",
		common.HexToAddress(withdrawRow.ToAddress),
		tokenBalance,
	)
	if err != nil {
		mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
		return err
	}
	rpcTx := types.NewTransaction(
		uint64(nonce),
		common.HexToAddress(tokenRow.TokenAddress),
		big.NewInt(0),
		uint64(gasLimit),
		big.NewInt(gasPrice),
		input,
	)
	signedTx, err := types.SignTx(rpcTx, types.NewEIP155Signer(big.NewInt(chainID)), key)
	if err != nil {
		return err
	}
	ts := types.Transactions{signedTx}
	rawTxBytes := ts.GetRlp(0)
	rawTxHex := hex.EncodeToString(rawTxBytes)
	txHash := strings.ToLower(signedTx.Hash().Hex())
	now := time.Now().Unix()
	_, err = app.SQLUpdateTWithdrawGenTx(
		context.Background(),
		dbTx,
		&model.DBTWithdraw{
			ID:           withdrawID,
			TxHash:       txHash,
			HandleStatus: app.WithdrawStatusHex,
			HandleMsg:    "hex",
			HandleTime:   now,
		},
	)
	if err != nil {
		return err
	}
	_, err = model.SQLCreateTSend(
		context.Background(),
		dbTx,
		&model.DBTSend{
			RelatedType:  app.SendRelationTypeWithdraw,
			RelatedID:    withdrawID,
			TxID:         txHash,
			FromAddress:  hotAddress,
			ToAddress:    withdrawRow.ToAddress,
			BalanceReal:  withdrawRow.BalanceReal,
			Gas:          gasLimit,
			GasPrice:     gasPrice,
			Nonce:        nonce,
			Hex:          rawTxHex,
			HandleStatus: app.SendStatusInit,
			HandleMsg:    "",
			HandleTime:   now,
		},
		false,
	)
	if err != nil {
		return err
	}
	// 处理完成
	err = dbTx.Commit()
	if err != nil {
		return err
	}
	isComment = true
	return nil
}

// CheckGasPrice 检测gas price
func CheckGasPrice() {
	lockKey := "EthCheckGasPrice"
	app.LockWrap(lockKey, func() {
		// 获取最高单价
		maxValue, err := app.SQLGetTAppStatusIntValueByK(
			context.Background(),
			xenv.DbCon,
			"max_gas_price_eth",
		)
		if err != nil {
			if !strings.Contains(err.Error(), "no app status int of") {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
		}
		if maxValue <= 0 {
			maxValue = 80000000000
			// 创建
			_, err := model.SQLCreateTAppStatusInt(
				context.Background(),
				xenv.DbCon,
				&model.DBTAppStatusInt{
					K: "max_gas_price_eth",
					V: maxValue,
				},
				true,
			)
			if err != nil {
				mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
				return
			}
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
			Proxy(xenv.Cfg.Proxy).
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
		toUserGasPrice := resp.Fast * int64(math.Pow10(8))
		toColdGasPrice := resp.Average * int64(math.Pow10(8))
		if toUserGasPrice > maxValue {
			toUserGasPrice = maxValue
		}
		if toColdGasPrice > maxValue {
			toColdGasPrice = maxValue
		}
		_, err = app.SQLUpdateTAppStatusIntByK(
			context.Background(),
			xenv.DbCon,
			&model.DBTAppStatusInt{
				K: "to_user_gas_price_eth",
				V: toUserGasPrice,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
		_, err = app.SQLUpdateTAppStatusIntByK(
			context.Background(),
			xenv.DbCon,
			&model.DBTAppStatusInt{
				K: "to_cold_gas_price_eth",
				V: toColdGasPrice,
			},
		)
		if err != nil {
			mcommon.Log.Errorf("err: [%T] %s", err, err.Error())
			return
		}
	})
}
