# InterPlanetary Delay Maker

L2透過型の惑星間通信遅延エミュレータ。火星通信（3〜22分）や月面通信（1.3秒）のシミュレーションに使用。

WIDE Project Space-WG

## アーキテクチャ

```
                    ┌──────────────────────────────────┐
┌─────────┐        │           Delay Box              │        ┌─────────┐
│  Earth  │eth0    │  ┌─────────┐    ┌─────────────┐  │   eth0 │  Mars   │
│10.0.0.2 │◄──────►│  │ Capture │───►│ Redis Queue │──┼───────►│10.0.0.3 │
│         │eth1    │  │ (pcap)  │    │ (Sorted Set)│  │   eth0 │         │
│         │◄──────►│  └─────────┘    └─────────────┘  │◄──────►└─────────┘
└─────────┘        │  veth-earth ◄─► veth-mars        │        ┌─────────┐
                   │  veth-earth-moon ◄─► veth-moon   │   eth0 │  Moon   │
                   └──────────────────────────────────┘◄──────►│10.1.0.3 │
                                                               └─────────┘
```

各リンク（Earth↔Mars, Earth↔Moon）は独立した遅延キューを持ち、ダッシュボードからリアルタイムで制御可能。

## クイックスタート

```bash
git clone https://github.com/shawsuzuki/InterPlanetary-DelayMaker.git
cd InterPlanetary-DelayMaker
docker compose up -d --build
```

ダッシュボードを開く: **http://localhost:8080**

## 実機モード (bare-metal)

3台の Linux マシン（例: R86S）をスイッチ経由で繋ぎ、中央機を遅延ボックスとして使う構成。中央機 (B機) の物理NIC に直接pcapする。

### 構成

```
                       ┌────────────────────────────┐
                       │   L2 Switch (VLAN対応)      │
                       └─┬────────┬──────────┬─────┘
                  VLAN 2 │ VLAN 6 │ VLAN 3   │
                  access │ tagged │ access   │
                         │ tagged │          │
                ┌────────┘  trunk └─────┐    │
                │                       │    │
        ┌───────┴──┐         ┌──────────┴────┴──┐         ┌──────────┐
        │  Earth   │         │   Delay box (B)   │         │   Mars   │
        │192.168.2.1│        │ enp1s0  enp2s0    │         │192.168.2.2│
        └──────────┘         │  mgmt   data plane│         └──────────┘
                             │   .4    (no IP)   │
                             └───────────────────┘

  VLAN 2: Earth ↔ delay (data, 192.168.2.0/24)
  VLAN 3: Mars  ↔ delay (data, 192.168.2.0/24 ← 同じサブネット)
  VLAN 6: mgmt 全機 (192.168.100.0/24)
```

Earth と Mars は IP的に同じ `/24` だが、L2では別 VLAN に分離されており delay box が L2透過にフレームを橋渡し（遅延付きで）する。

### スイッチ側の設定（前提）

| 接続先 | switchport モード | VLAN |
|---|---|---|
| Earth host | access | VLAN 2 |
| Mars host | access | VLAN 3 |
| Delay box `enp1s0` (mgmt) | trunk | VLAN 6 tagged |
| Delay box `enp2s0` (data) | trunk | VLAN 2, 3 tagged |
| 他機の mgmt port | trunk | VLAN 6 tagged |

### B機 セットアップ手順

#### 1. リポジトリ取得 + Netplan 設定（mgmt IP）

```bash
git clone https://github.com/shawsuzuki/InterPlanetary-DelayMaker.git
cd InterPlanetary-DelayMaker

sudo cp bare-metal/netplan/2nic.yaml /etc/netplan/01-delaybox.yaml
sudo chmod 600 /etc/netplan/01-delaybox.yaml
sudo nano /etc/netplan/01-delaybox.yaml     # IP・GW・DNS・NIC名を実機に合わせる
sudo netplan apply
```

これで mgmt（VLAN 6 tagged → `enp1s0.6`）に `192.168.100.4/24` がつき ssh 到達可能になる。

| ファイル | 用途 |
|---|---|
| [bare-metal/netplan/2nic.yaml](bare-metal/netplan/2nic.yaml) | enp1s0=mgmt(VLAN 6), enp2s0=data plane（推奨） |
| [bare-metal/netplan/1nic.yaml](bare-metal/netplan/1nic.yaml) | enp2s0 1本に VLAN 2/3/6 全集約 |

> データプレーン NIC・サブIF には **IP を振らない**（L2透過の前提）。VLAN 2/3 用サブIF (`enp2s0.2` / `enp2s0.3`) は `boot.sh` が動的に生成するので netplan には書かない。

#### 2. `.env` で NIC と VLAN を指定

```bash
cp bare-metal/.env.example .env
nano .env
```

1本トランク構成（推奨）の最小設定:

```bash
EARTH_IFACE=enp2s0
MARS_IFACE=enp2s0
EARTH_VLAN=2
MARS_VLAN=3
DELAY_EARTH_TO_MARS=10
DELAY_MARS_TO_EARTH=10
```

#### 3. 初回起動

```bash
sudo ./bare-metal/setup.sh
```

これで Dockerイメージビルド → NIC設定 → サブIF生成 → Redis/delaybox/dashboard 起動。

**ダッシュボード**: `http://192.168.100.4:8080`

#### 4. 自動起動 (systemd) を有効化

```bash
sudo ./bare-metal/install-systemd.sh
```

再起動後も自動でNIC再構成 + コンテナ起動する。

### エンドポイント側 (Earth / Mars) の設定

それぞれの host のスイッチポートが access VLAN 2 / 3 なら、ホスト側はタグなしで `192.168.2.x/24` を載せるだけ。例:

```bash
# Earth ホスト
sudo ip addr add 192.168.2.1/24 dev <NIC>
sudo ip link set <NIC> up

# Mars ホスト
sudo ip addr add 192.168.2.2/24 dev <NIC>
sudo ip link set <NIC> up
```

永続化は Netplan/NetworkManager 等で。

### 動作確認

B機で:

```bash
# サブIF が作成されていること
ip -d link show enp2s0.2          # vlan id 2 が見える
ip -d link show enp2s0.3          # vlan id 3 が見える
ip -br addr show enp2s0           # IP無し（生NICには付けない）

# トランクから VLAN 2/3 tagged フレームが届いていること
sudo tcpdump -nei enp2s0 -e vlan -c 20

# delaybox のキャプチャ
docker compose -f docker-compose.bare.yml logs -f delaybox

# 状態確認
sudo systemctl status delaybox
journalctl -u delaybox -f
```

Earth ホストから:

```bash
# 10秒遅延設定なら ARP往復 + ICMP往復 で約40秒で初応答
ping 192.168.2.2
```

### 本番運用 (初回セットアップ後の日常操作)

初回 `setup.sh` + `install-systemd.sh` を済ませてあれば、再起動後は自動で起動する。手動操作は systemd で:

```bash
sudo systemctl start delaybox       # 起動
sudo systemctl stop delaybox        # 停止 (compose down も実行)
sudo systemctl restart delaybox     # .env 変更後など
sudo systemctl status delaybox      # 状態確認
journalctl -u delaybox -f           # ログ追跡
```

**ダッシュボード**: `http://<B機mgmt-IP>:8080` （例: http://192.168.100.4:8080）

### コールドスタート（電源オフ → 再開の最短手順）

展示の朝など、一度電源を落としたあとの復帰手順。初回の `setup.sh` + `install-systemd.sh` が済んでいれば、基本は **電源を入れて待つだけ**。

1. **3台の電源を入れる**（Earth / Delay box(B機) / Mars）。B機は systemd が自動で NIC 再設定＋コンテナ起動するので **30〜60秒待つ**。

2. **B機が起動したか確認**
   ```bash
   sudo systemctl status delaybox                  # active なら OK
   docker compose -f docker-compose.bare.yml ps    # redis / delaybox / dashboard が Up
   ```

3. **遅延値を確認**（再起動すると `.env` の初期値が再適用され、キューは空で始まる）
   ```bash
   docker exec redis redis-cli MGET config:delay_to_mars config:delay_to_earth
   # 想定値（例: "240" "240"）ならOK。違えば .env を直して  sudo systemctl restart delaybox
   ```
   > 恒久的なデフォルトは `.env` の `DELAY_EARTH_TO_MARS` / `DELAY_MARS_TO_EARTH`。Redis CLI やダッシュボードでの変更は「実行中の値」なので、再起動すると `.env` 値に戻る。

4. **Earth / Mars のIP**（Netplan等で永続化済みなら不要。していなければ再設定）
   ```bash
   sudo ip addr add 192.168.2.1/24 dev <NIC> && sudo ip link set <NIC> up   # Earth
   sudo ip addr add 192.168.2.2/24 dev <NIC> && sudo ip link set <NIC> up   # Mars
   ```

5. **static ARP**（遅延が60秒超なら必須。再起動で ARP テーブルは消える）
   ARP固定をサービス化していれば各ホスト起動時に自動投入される。手動なら相手のMACを入れる（MACは `ip link show <NIC>` で確認）:
   ```bash
   sudo ip neigh replace 192.168.2.2 lladdr <MarsのMAC>  dev <NIC> nud permanent   # Earth で
   sudo ip neigh replace 192.168.2.1 lladdr <EarthのMAC> dev <NIC> nud permanent   # Mars で
   ```

6. **疎通確認**
   ```bash
   docker exec redis redis-cli ZCARD delay:to_mars     # パケットが積まれていくか
   ```
   ダッシュボード: `http://192.168.100.4:8080`

> 自動起動が効いていない／手動で上げたいときは `sudo systemctl start delaybox`（systemd未導入なら `sudo ./bare-metal/boot.sh`）。

### 遅延時間の変更

#### 方法1: ダッシュボード (推奨)

ブラウザで `http://<B機>:8080` を開く:
- **Settings タブ → Mars Delay**: Demo (5s) / Closest (182s) / Farthest (1338s) プリセット
- **Settings タブ → Dynamic Delay**: 連続的に増減（例: 100s → 1338s を毎秒+5s で変化）でリアルな軌道変化を再現
- 設定変更は1秒以内に反映、再起動不要

#### 方法2: Redis CLI (B機上)

```bash
# 火星: 最接近 (3分2秒 = 182秒)
docker exec redis redis-cli SET config:delay_to_mars 182
docker exec redis redis-cli SET config:delay_to_earth 182

# 火星: 最遠 (22分18秒 = 1338秒)
docker exec redis redis-cli SET config:delay_to_mars 1338
docker exec redis redis-cli SET config:delay_to_earth 1338

# デモ用 (10秒)
docker exec redis redis-cli SET config:delay_to_mars 10
docker exec redis redis-cli SET config:delay_to_earth 10

# 現在値の確認
docker exec redis redis-cli GET config:delay_to_mars
```

変更は1秒以内に delaybox が拾って反映。

#### 方法3: 起動時のデフォルト値変更

repo root の `.env` を編集して `systemctl restart delaybox`:
```bash
DELAY_EARTH_TO_MARS=600
DELAY_MARS_TO_EARTH=600
```

### その他のコマンド

```bash
# キューを全クリア（過去パケット破棄）
docker exec redis redis-cli DEL delay:to_mars delay:to_earth
# またはダッシュボードの "Flush All Queues" ボタン

# 静的ARP（Earth/Mars 各ホスト、long delay 時必須）
# Earth で
sudo ip neigh replace <相手IP> lladdr <相手MAC> dev vlan.2 nud permanent

# アンインストール
sudo ./bare-metal/install-systemd.sh --uninstall
sudo docker compose -f docker-compose.bare.yml down -v
```

> **長時間遅延の注意**: Linux のARPテーブルは60秒で老化するため、遅延が60秒を超える場合は両ホストで **static ARP (`nud permanent`)** を必ず設定する。設定しないと60秒ごとに数十秒間 ping が `Destination Host Unreachable` になる。

### トラブルシューティング (実機モード)

| 症状 | 確認 |
|---|---|
| ssh が通らない | `netplan apply` 済み? mgmt スイッチポートで VLAN 6 tagged 許可済み? |
| ping が通らない | `sudo tcpdump -nei enp2s0 -e vlan` で VLAN 2/3 tagged フレーム流れてるか / `docker logs delaybox` で `Queued` |
| サブIFができない | `ip link show enp2s0` で UP か / `journalctl -u delaybox` で boot.sh のログ |
| 再起動後動かない | `systemctl status delaybox` / `.env` が repo root にあるか |
| 遅延が効かない | `docker exec redis redis-cli GET config:delay_to_mars` |

## 動作確認

```bash
# Mars へ ping（デフォルト10秒遅延 → RTT約20秒）
docker exec earth ping -W 30 10.0.0.3

# Moon へ ping（デフォルト1秒遅延 → RTT約2秒）
docker exec earth ping -W 5 10.1.0.3

# ログ確認
docker compose logs -f delaybox
```

> **注意:** 初回pingはARP解決に往復分の時間がかかります（Mars 10s遅延の場合、ARP往復で約20秒 + ICMP往復で約20秒 = 約40秒）

## ダッシュボード

`http://localhost:8080` でWebダッシュボードにアクセス。

機能:
- Mars / Moon / Custom 各リンクの遅延とキュー状態をリアルタイム表示（1秒更新）
- パケット可視化: レーン上にパケットの進行状況をタイプ別にカラー表示
- パケットキャプチャログ: 方向別フィルタ付きのリアルタイムログ
- **プリセット:** Demo (5s/1s), Moon Only (1.3s), Mars closest (3m2s), Mars farthest (22m22s)
- カスタム遅延の入力と即時適用
- 動的遅延変更（Ramp）: 一定量ずつ増加/減少、またはパーセントで変化

## 遅延時間の動的変更

### ダッシュボードから

プリセットボタンをクリック、またはカスタム値を入力。

### Redis CLI から

```bash
# 火星: 最接近（3分）
docker exec redis redis-cli SET config:delay_to_mars 182
docker exec redis redis-cli SET config:delay_to_earth 182

# 月面: リアルな遅延（1.3秒）
docker exec redis redis-cli SET config:delay_to_moon 1.3
docker exec redis redis-cli SET config:delay_from_moon 1.3
```

変更は1秒以内に自動反映（再起動不要）。

### docker-compose.yml から（起動時）

```yaml
environment:
  - DELAY_EARTH_TO_MARS=1200    # 20分（火星遠方）
  - DELAY_MARS_TO_EARTH=1200
  - DELAY_EARTH_TO_MOON=1       # 1秒（月面）
  - DELAY_MOON_TO_EARTH=1
```

## ネットワーク構成

| コンテナ | インターフェース | IPアドレス | サブネット |
|---------|--------------|----------|---------|
| Earth | eth0 | 10.0.0.2 | 10.0.0.0/24 (Mars link) |
| Earth | eth1 | 10.1.0.2 | 10.1.0.0/24 (Moon link) |
| Mars | eth0 | 10.0.0.3 | 10.0.0.0/24 |
| Moon | eth0 | 10.1.0.3 | 10.1.0.0/24 |

## パケット分類

ダッシュボードの可視化・ログでは、パケットをEthernetフレームから分類して表示します。

| 分類 | 条件 | 可視化 | ログ |
|------|------|--------|------|
| ARP | EtherType 0x0806, sender IP ≠ target IP | ● 黄 | ✅ |
| otherARP | EtherType 0x0806, sender IP = target IP (GARP等) | ● 橙 | ✅ |
| ICMP | IPv4 Protocol 1 | ● 青丸 | ✅ |
| ICMPv6 | IPv6 NH 58, Type ≠ 133-137 (Echo等) | ▲ 青三角 | ✅ |
| TCP | IPv4/IPv6 Protocol 6 | ● 赤 | ✅ |
| UDP | IPv4/IPv6 Protocol 17 | ● 緑 | ✅ |
| Other | 上記以外 | ● 灰 | ✅ |
| NDP | IPv6 NH 58, Type 133-137 (RS/RA/NS/NA/Redirect) | — | — |

> **NDP (Neighbor Discovery Protocol) について:**
> ICMPv6 Type 133-137 のNDPパケット（RS, RA, NS, NA, Redirect）は、IPv6のアドレス解決やルータ探索のためのマネジメントパケットです。
> これらは **遅延キューを通過してL2遅延は受けますが、ダッシュボードの可視化およびパケットキャプチャログには表示されません**。
> ユーザ通信（ping6等のICMPv6 Echo）と区別するため、意図的に棄却しています。

## サンプルファイル

`samples/` ディレクトリがEarthコンテナに `/samples` としてマウントされています。

```bash
# Mars へ netcat で転送
docker exec mars sh -c "nc -l -p 9000 > /tmp/received.jpeg" &
docker exec earth sh -c "nc 10.0.0.3 9000 < /samples/mars_sol0.jpeg"
```

## キュー状態の確認

```bash
docker exec redis redis-cli ZCARD delay:to_mars
docker exec redis redis-cli ZCARD delay:to_earth
docker exec redis redis-cli ZCARD delay:to_moon
docker exec redis redis-cli ZCARD delay:from_moon
docker exec redis redis-cli INFO memory | grep used_memory_human
```

## 停止・リセット

```bash
# 停止
docker compose down

# 完全リセット（キューもクリア）
docker compose down -v && docker compose up -d --build
```

## ファイル構成

```
.
├── README.md               # このファイル
├── CLAUDE.md               # Claude Code 用コンテキスト
├── docker-compose.yml      # コンテナ定義（Docker模擬モード）
├── docker-compose.bare.yml # 実機モード（host network + 物理NIC）
├── bare-metal/
│   ├── setup.sh            # 初回セットアップ (.env生成 + build + boot.sh)
│   ├── boot.sh             # 起動毎 NIC再設定 + compose up (systemdからも使用)
│   ├── install-systemd.sh  # systemd unit インストール/アンインストール
│   ├── delaybox.service    # systemd unit テンプレート
│   ├── .env.example        # NIC名・VLAN・遅延初期値のテンプレート
│   └── netplan/
│       ├── 2nic.yaml       # 2NIC構成: mgmt VLAN 6 tagged + data plane
│       └── 1nic.yaml       # 1NIC構成: VLAN 2/3/6 全部 tagged で1本に集約
├── delaybox/
│   ├── Dockerfile          # Alpine + Go + libpcap
│   ├── entrypoint.sh       # Docker模擬モード用 veth pair 設定
│   ├── entrypoint-bare.sh  # 実機モード用 (env→flag変換のみ)
│   ├── go.mod / go.sum     # Go モジュール
│   ├── main.go             # L2遅延デーモン（link抽象化）
│   └── main_test.go        # ユニットテスト
├── dashboard/
│   ├── Dockerfile          # マルチステージビルド
│   ├── go.mod / go.sum     # Dashboard 依存関係
│   ├── main.go             # HTTP API サーバ
│   └── index.html          # ダッシュボード SPA
└── samples/
    ├── README.md            # デモ手順
    └── mars_sol0.jpeg       # テスト画像
```

## トラブルシューティング

| 症状 | 確認方法 |
|------|---------|
| パケットが流れない | `docker exec redis redis-cli ZCARD delay:to_mars` でキュー確認 |
| ARP解決しない | 遅延の2倍待つ必要あり（ARP往復） |
| vethが作成されない | `docker compose logs delaybox` でentrypoint確認 |
| Moonが表示されない | `docker inspect -f '{{.State.Pid}}' moon` でコンテナ確認 |
| ダッシュボード接続エラー | Redisが起動しているか `docker compose ps` で確認 |
