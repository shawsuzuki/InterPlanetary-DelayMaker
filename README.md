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
- Mars / Moon 各リンクの遅延とキュー状態をリアルタイム表示（1秒更新）
- **プリセット:** Demo (5s/1s), Moon Only (1.3s), Mars closest (3m2s), Mars farthest (22m22s)
- カスタム遅延の入力と即時適用

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

## サンプルファイル

`samples/` ディレクトリにデモ用の火星ローバー画像があります。

```bash
# サンプル画像をEarthコンテナにコピー
docker cp samples/mars_sol0.jpeg earth:/tmp/

# Mars へ netcat で転送
docker exec mars sh -c "nc -l -p 9000 > /tmp/received.jpeg" &
docker exec earth sh -c "nc 10.0.0.3 9000 < /tmp/mars_sol0.jpeg"
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
├── docker-compose.yml      # コンテナ定義
├── delaybox/
│   ├── Dockerfile          # Alpine + Go + libpcap
│   ├── entrypoint.sh       # veth ペア設定（Mars必須, Moon自動検出）
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

## ION-DTN統合

Earth/Marsコンテナに ION-DTN をインストールして使用:

```bash
docker exec -it earth sh
# ION の設定ファイルを配置して ionstart

docker exec -it mars sh
# ION の設定ファイルを配置して ionstart
```

LTP の OWLT 設定に注意:
- `ltprc` の `ownQtime` / `remoteQtime` を遅延時間に合わせて設定

## トラブルシューティング

| 症状 | 確認方法 |
|------|---------|
| パケットが流れない | `docker exec redis redis-cli ZCARD delay:to_mars` でキュー確認 |
| ARP解決しない | 遅延の2倍待つ必要あり（ARP往復） |
| vethが作成されない | `docker compose logs delaybox` でentrypoint確認 |
| Moonが表示されない | `docker inspect -f '{{.State.Pid}}' moon` でコンテナ確認 |
| ダッシュボード接続エラー | Redisが起動しているか `docker compose ps` で確認 |
