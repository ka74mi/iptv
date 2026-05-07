# iptv
EDCB の TCP API を利用して M3U プレイリストと XMLTV 形式の EPG を生成し、SrvPipe 経由で IPTV ストリーム配信を行うサービスです。

M3U/XMLTV に対応した IPTV クライアントで地上波・BS/CS の視聴ができます。

## 仕組み
```
[EDCB コンテナ]                        [iptv コンテナ]
EpgDataCap_Bon
  ↕ TCP API (port 4510)  ←→  M3U・XMLTV 生成・ロゴ配信
  ↕ SrvPipe (FIFO)       ←→  TS ストリーム転送 → tsreadex → [IPTV クライアント]
```
1. EDCB の TCP API (port 4510) からサービス一覧・EPG 情報を取得して M3U・XMLTV を生成します
2. 視聴リクエスト時に EDCB へチャンネル設定コマンドを送り、EpgDataCap_Bon を NetworkTV モードで起動します
3. EDCB の SrvPipe 経由で TS データを受け取り、tsreadex を通した後に HTTP クライアントに転送します

## 事前準備 (EDCB)

- Linux ネイティブ版 EDCB が動作していて EPG の取得が完了している
- スクランブル解除済みの TS ストリームを出力できる
- EpgTimerSrv の TCP API (port 4510) が有効になっている
- [設定メニュー] → [ネットワーク設定] → TCP 送信先に「SrvPipe」を追加済み
- [設定メニュー] → [視聴に使用する BonDriver] で各 BonDriver を追加済み

## 導入方法

### 1. イメージのビルド
```bash
$ git clone --depth 1 https://github.com/ka74mi/iptv
$ cd iptv
$ podman build -t iptv .
```

### 2. コンテナの起動
```bash
$ podman run -d \
  --name iptv \
  --network <EDCB と同じネットワーク> \
  -p 8080:8080 \
  -e EDCB_HOST=<EDCB のホスト名または IP> \
  -e BASE_URL=http://<このサービスの IP またはホスト名>:8080 \
  iptv
```

#### 環境変数
| 変数名 | 説明 | デフォルト | 例 |
|--------|------|------------|----|
| `EDCB_HOST` | EDCB のホスト名または IP アドレス | `edcb` | `edcb` / `192.168.0.100` |
| `EDCB_PORT` | EDCB の TCP API ポート番号 | `4510` | `4510` |
| `BASE_URL` | M3U プレイリストに IPTV クライアントから到達できる URL を指定します。`http://` または `https://` から始める必要があります | `http://localhost:8080` | `http://192.168.0.100:8080` / `https://your-host.ts.net` |

### 3. 起動例

#### podman run
```bash
$ podman run -d \
  --name iptv \
  --network dtv-network \
  -p 8080:8080 \
  -e EDCB_HOST=edcb \
  -e BASE_URL=http://192.168.0.100:8080 \
  iptv
```

#### Podman Quadlet
```ini
# ~/.config/containers/systemd/iptv.container

[Unit]
Description=EDCB IPTV service
After=edcb.service
Requires=edcb.service

[Container]
Image=localhost:5000/iptv:latest
AutoUpdate=registry
ContainerName=iptv
Network=dtv-network
PublishPort=8080:8080
Environment=EDCB_HOST=edcb
Environment=BASE_URL=http://192.168.0.100:8080

[Service]
Restart=on-failure

[Install]
WantedBy=default.target
```

## エンドポイント
| パス | 説明 |
|------|------|
| `/playlist` | M3U プレイリスト |
| `/epg` | XMLTV 形式の EPG |
| `/stream/{onid}/{tsid}/{sid}` | TS ストリーム |

## IPTV クライアントへの設定
M3U プレイリストの URL を登録してください：
```
<BASE_URL>/playlist
```
XMLTV の EPG URL を登録してください：
```
<BASE_URL>/epg
```

## セキュリティ
このツールは信頼できるローカルネットワーク (LAN) 内での利用を想定しています。

外部からアクセスする場合は VPN の利用を推奨します。

## 免責事項
このプロジェクトは Claude との対話を通じて開発されています。

動作の正確性・安全性について作者は責任を負いません。自己責任でご利用ください。

## ライセンス
[MIT License](LICENSE)

## 謝辞
TCP API を通じた EDCB 連携は、xtne6f 氏による EDCB およびその TCP API の実装があって初めて成立しています。

また、同氏が開発する [tsreadex](https://github.com/xtne6f/tsreadex) を TS ストリームの音声処理に利用しています。

ストリーム配信におけるシーケンス部の実装にあたって、[KonomiTV](https://github.com/tsukumijima/KonomiTV) のソースコードを参考にさせていただきました。

