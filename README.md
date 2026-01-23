# Long Delay Network Emulator (Mars Communication Simulator)

L2透過型の長時間遅延エミュレータ。火星通信（6〜40分遅延）のシミュレーションに使用。

## アーキテクチャ

```
┌─────────┐     ┌─────────────────────────────┐     ┌─────────┐
│  Earth  │     │         Delaybox            │     │  Mars   │
│10.0.0.2 │────▶│ veth-earth ◀──▶ veth-mars  │◀────│10.0.0.3 │
│         │     │         │                   │     │         │
└─────────┘     │         ▼                   │     └─────────┘
                │   Redis Sorted Set          │
                │   (timestamp → frame)       │
                └─────────────────────────────┘
```

## 使用方法

### 起動

```bash
docker-compose up -d
```

### 動作確認

```bash
# Earth から Mars へ ping
docker exec -it earth ping 10.0.0.3

# 最初の応答: 約40秒後（ARP往復 + ICMP往復）
# 2回目以降: 約20秒（ARPキャッシュ済み）
```

### ログ確認

```bash
docker-compose logs -f delaybox
```

### 遅延時間の変更

`docker-compose.yml` の環境変数を編集:

```yaml
environment:
  - DELAY_EARTH_TO_MARS=1200    # 20分
  - DELAY_MARS_TO_EARTH=1200    # 20分
```

### 停止

```bash
docker-compose down -v
```

## VLAN対応（将来拡張）

VLANタグに基づいて異なる遅延を設定する機能は `main.go` の `parseVLAN()` 関数を拡張して実装可能。

```go
func getDelayForVLAN(vlanID uint16, direction string) time.Duration {
    switch vlanID {
    case 100:  // 地球-月
        return 1 * time.Second
    case 200:  // 地球-火星
        return 1200 * time.Second
    default:
        return 10 * time.Second
    }
}
```

## ION-DTN統合

Earth/Marsコンテナに ION-DTN をインストールして使用:

```bash
# Earth コンテナで ION を起動
docker exec -it earth sh
# ION の設定ファイルを配置して ionstart

# Mars コンテナで ION を起動
docker exec -it mars sh
# ION の設定ファイルを配置して ionstart
```

LTP の OWLT 設定に注意:
- `ltprc` の `ownQtime` / `remoteQtime` を遅延時間に合わせて設定

## トラブルシューティング

### コンテナが起動しない

```bash
docker-compose down -v
docker-compose up -d
```

### veth が作成されない

```bash
docker exec -it delaybox ip link show
# veth-earth, veth-mars が表示されるか確認
```

### パケットが流れない

```bash
# Redis キューを確認
docker exec -it redis redis-cli ZCARD delay:to_mars
docker exec -it redis redis-cli ZCARD delay:to_earth
```

## ファイル構成

```
longdelay/
├── docker-compose.yml      # コンテナ定義
├── README.md               # このファイル
└── delaybox/
    ├── Dockerfile          # delaybox イメージ
    ├── entrypoint.sh       # ネットワーク設定スクリプト
    ├── go.mod              # Go モジュール定義
    └── main.go             # L2遅延デーモン
```
