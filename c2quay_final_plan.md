# c2quay 実装計画 最終改訂版

最終更新: 2026-04-17  
ステータス: 実装着手前の最終版  
前バージョン: レビュー改訂案 (2026-04-17)

---

## 0. この改訂の意図

前回のレビューによる改訂案は、**実装に入る前に直しておくべき論点を的確に捉えています**。特に以下の三点は大きな方向転換でありながら、いずれも正当な指摘です。

第一に、Composeの解決処理を`compose-go`ライブラリ経由の自前読み取りから、`docker compose config --format json`を通じたCLI経由のsource-of-truthに寄せるという判断は、**将来のCompose機能追加に対する追従コストを劇的に下げます**。第二に、リリース識別子においてmutable tagを排除し、不変のmanifestまたはdigestを中心に据えるという判断は、**Pactの判定ロジックの信頼性を根本から保証します**。第三に、「atomic」ではなく「restart-safe / auditable」を目標に置き直した点は、**Composeの実態に即した正直な表現**への修正です。

ただし、最新のWeb検索結果を踏まえて**いくつかの事実確認と追加の精緻化**が必要です。以下の点は改訂案をそのまま採用するよりも、もう一歩踏み込んだ形にすべきだと判断しました。

1. **Compose v5 SDKの位置付け**が単なる噂ではなく公式事実であることを明確にし、同時に**CVE-2025-62725**への対処方針を計画段階で決めておくこと。
2. **`docker compose up --wait`には既知のバグ**（init containerがexit 0で終わってもコマンド全体がexit 1を返す）があり、これを無視した設計は運用時に誤判定を生むこと。
3. **Pact Broker APIがHAL駆動である**という仕様を、adapter設計の中で単に隠蔽するだけでなく、HALの`_links`を実際に辿るクライアント設計として組み込むこと。
4. **Go 1.25で安定化した`testing/synctest`**と**`log/slog`の`GroupAttrs`**は、c2quayのテストと観測可能性を大幅に改善するため、技術スタックに明示的に組み込むこと。開発基準はGo 1.26.2（最新パッチ）とし、サポート下限はGo 1.25に据えることで、公式サポート範囲内の最広ユーザー環境をカバーする。
5. **Docker Compose v1の完全廃止（2025年4月）**を受けて、c2quayが`docker-compose`（ハイフン形式）を一切サポートしないことを設計判断として明示すること。

以下、これらを織り込んだ最終形を提示します。

---

## 1. 変更サマリー

改訂案から最終版への主な変更点は以下の通りです。

| 項目 | 改訂案 | 最終版 |
|------|--------|--------|
| Compose CLI下限バージョン | 明示なし | v2.40.2以上（CVE対応） |
| `docker compose`バイナリ形式 | 明示なし | ハイフンなし形式のみサポート |
| `up --wait`の扱い | 全面採用 | 採用するがinit containerの誤判定ケースを明記 |
| Broker APIクライアント | endpoint adapter | HAL driven client |
| Goバージョン | 1.25基準, 1.24下限 | **1.26.2基準, 1.25下限**（Pact-Goと同調、公式サポート最新2リリースに合わせる） |
| testing/synctest | 言及なし | 時間依存テストで明示採用 |
| エラー時の観測可能性 | 一般的な記述 | `slog.GroupAttrs`で構造化失敗情報 |

---

## 2. 設計原則（確定版）

前回レビューで提示された原則を踏襲しつつ、表現を最終化します。

### 2.1 c2quayはゲートである

c2quayは、ビルド・配布・オーケストレーション全体を吸収しません。責務は以下に限定されます。

- 対象サービスとそのリリース識別子を解決する
- Pact Brokerに対してdeploy可否を判定する
- 通過した場合のみ`docker compose`実行を許可する
- 実行後に`record-deployment`を呼ぶ
- 実行結果を監査可能な形で残す

この原則はKamalとの差別化の核でもあります。Kamalが「デプロイライフサイクル全体のオーケストレーター」であるのに対し、c2quayは**意図的にその責務を取りません**。

### 2.2 実行時の真実は「インストール済みの`docker compose`」である

Composeの仕様は継続的に変化しています。Compose v2とv5はCompose Specificationを使用しており、Compose v1とは異なり`version`トップレベル要素を無視し、Compose Specificationに完全に依拠してファイルを解釈します。したがって、c2quayが独自のCompose解釈エンジンを持つことは、**ユーザー環境との解釈差異というリスクを永続的に抱え込む**ことを意味します。

この意思決定を技術的に裏付ける追加の根拠として、**Composeには急速に追加される新機能**があります。例えば`include`指示子はOCIおよびGit参照をサポートするようになり、`models`という新しいトップレベル要素がAIモデル統合のために追加されました。これらをc2quay側で追う労力は、ゲーティングという主目的から見て完全に無駄です。

したがって、設定解決・サービス列挙・イメージ列挙の主経路はCLI経由とします。

```bash
docker compose -f ... -p ... config --quiet
docker compose -f ... -p ... config --format json
docker compose -f ... -p ... ps --format json
```

### 2.3 リリース識別子は不変でなければならない

Pactのdeploy判定は「どのバージョンを出すのか」が一意かつ不変であることを前提としています。pacticipant versionはバージョン番号で識別され、典型的にはgit shaまたはリポジトリ参照をメタデータとして持つセマンティックバージョン番号になります。pact または verification resultが公開されるたびに、pacticipant version resourceが自動的に作成されます。

つまり、Pact Brokerは「同じバージョン番号なら同じ内容」という仮定で動作しています。mutable tagを使うとこの仮定が壊れ、判定そのものが信頼できなくなります。

優先順位は以下とします。

1. **manifest file**（CIが吐く不変version/digest一覧）
2. **resolved image digest**（Compose解決結果から抽出）
3. **git SHA**（全サービス共通コミットで束ねられる場合のみ）

`image_tag`戦略は**v0では実装を見送ります**。残すとしても`immutable_image_tag`という名称にして、`latest`や数値プレフィクスのない文字列タグを明示的に拒否する実装でない限り、危険性が利便性を上回ります。

### 2.4 目標はatomicではなくrestart-safe / auditable

Composeによるデプロイは根本的にトランザクショナルではありません。`docker compose up -d`の途中でネットワーク断が起きれば、一部のコンテナだけが再起動した状態で終わる可能性があります。

c2quayが保証すべきなのは以下です。

- **再実行可能性**: 同じ入力で再実行しても安全であること
- **状態差分の把握**: 実行前後の状態がデプロイ結果として記録されること
- **ロールバック判断材料**: 失敗時に「何が直前のバージョンだったか」を出力すること
- **記録の誠実さ**: `record-deployment`を誤って先行記録しないこと

---

## 3. 技術スタック（確定版）

### 3.1 Goバージョン

**開発基準**: Go 1.26.2  
**サポート下限**: Go 1.25  
**CI行列**: 1.25, 1.26

Go 1.26は2026年2月10日にリリースされ、Go 1.25の6ヶ月後にあたる最新メジャーバージョンです。現時点（2026年4月17日）で、Go 1.26.2が2026-04-07にリリースされ、go command、compiler、archive/tar、crypto/tls、crypto/x509、html/template、osパッケージへのセキュリティ修正と、go command、go fix command、compiler、linker、runtime、net、net/http、net/urlパッケージへのバグ修正が含まれています。c2quayの開発ではこのGo 1.26.2を基準とし、最新のセキュリティ修正を取り込んだ状態で実装を進めます。

サポート下限としてGo 1.25を採用する理由は二つあります。第一に、Go 1.26のリリースから約2ヶ月しか経過していない現時点では、多くの企業CI環境がまだGo 1.25で運用されており、ここでGo 1.26を強制すると「c2quayを使いたいがCIのGoバージョンを上げられない」という採用障壁を生んでしまいます。第二に、Goの公式サポートポリシーは最新2リリースをサポート対象とするため、1.25と1.26の両方を対象にすることで、**公式サポート範囲内で最も広いユーザー環境をカバー**できます。

この基準は**Pact-Goエコシステムとも整合します**。Pact-Go v2.4.1は最小Goバージョンを1.23に義務付けており、Goのeolスケジュールに従っているため、c2quayが1.25/1.26をサポートすることで、ユーザー環境でPact-Goと併用されても衝突しません。

Go 1.26で導入された主要な改善のうち、c2quayに直接関係するものを挙げておきます。まずGreen Teaガベージコレクタが既定で有効化され、メモリ局所性とCPUスケーラビリティが改善されました。c2quayのようなCLIツールでは起動時間やメモリ使用量が直接ユーザー体験に影響するため、この改善はそのまま恩恵になります。次にcgoコールのオーバーヘッドが約30%削減されましたが、c2quayはcgoを使わないため直接の影響はありません。ただしGoの標準ライブラリの一部（crypto関連など）がcgoを内部で使う場合があるため、間接的な恩恵は期待できます。またbuiltin `new`関数が式を引数に取れるようになり、Pact Broker APIのオプショナルフィールドを扱うコードが簡潔になります。ただしこれはサポート下限の1.25では使えないため、本体コードでは採用せず、内部ツールのみで利用する方針とします。

なお、Go 1.26からgo mod initが新しいgo.modファイルに指定するgoバージョンが**一つ前のメジャーバージョン**になる仕様変更があります。Go 1.26とそのマイナーリリースではgo 1.25.0を指定するgo.modファイルが作成されるため、この仕様は現在サポート中のGoバージョンと互換性のあるモジュールを作成することを促す目的があります。c2quayのgo.modもこの新しい慣習に従い、`go 1.25.0`を指定します。

### 3.2 ランタイム依存（確定版）

**必須外部依存**

- `github.com/spf13/cobra`: CLIフレームワーク
- `gopkg.in/yaml.v3`: 設定ファイルパース

**標準ライブラリで賄うもの**

- `net/http`: Pact Broker通信
- `encoding/json`: JSON処理
- `os/exec`: Composeサブプロセス実行
- `log/slog`: 構造化ログ
- `context`: キャンセル伝搬
- `sync`: 並行制御
- `testing/synctest`: 時間依存テスト（Go 1.25で安定化）

**テスト依存**

- `github.com/stretchr/testify`: アサーション
- `github.com/testcontainers/testcontainers-go`: 統合テスト

### 3.3 あえて採用しないもの

**`compose-go`ライブラリ**: ランタイムの主軸には据えません。改訂前の計画では「Composeファイルのパースには必ず`compose-go`を使う」としていましたが、これは方針転換により不要になります。ただし、将来的に「ユーザーが`c2quay validate`でComposeファイルの静的検証を行いたい」という要望が出た場合の**オプション依存**として保留します。

**`docker/compose/v2` SDK**: Compose v5は2025年にリリースされ、機能的にはCompose v2と同一ですが、公式Go SDKを導入しており、これによりCLIに依存せずにCompose機能を直接Goアプリケーションに統合できます。この事実は重要ですが、c2quayのv0では採用しません。理由は以下です。

- ユーザー環境の`docker compose`と埋め込みSDKのバージョン差が解釈差になる
- `docker/cli`への強依存を引き受ける必要がある
- ビルド時間とバイナリサイズが増える

ただし、**将来的な拡張ポイントとしては残します**。内部的にはcompose操作をadapter経由にすることで、SDK実装を後から差し込める構造にしておきます。

**`github.com/spf13/viper`**: 設定ファイルの多様な入力源をまとめるライブラリですが、c2quayは`c2quay.yml`一枚と環境変数のみを扱うため、Viperの抽象化はオーバーキルです。`yaml.v3`直読みで十分です。

### 3.4 重要な互換性制約

**Docker Compose CLIバージョン下限**: **v2.40.2以上**を要求します。これはCVE-2025-62725（`docker compose ps`からシステム侵害に至るincludeのパストラバーサル脆弱性）がv2.40.2以降で修正されているためです。c2quayは`doctor`コマンドで`docker compose version`の出力を検査し、下限を満たさない場合は警告します。

**`docker-compose`（ハイフン形式）**: **サポートしません**。Docker Compose v1（docker-compose）は2025年4月にGitHub Actionsランナーとofficial Dockerイメージから完全に削除されました。パイプラインがハイフンつきdocker-composeを参照している場合、壊れているか近いうちに壊れます。c2quayは`docker compose`（スペース形式）のみを呼び出し、ハイフン形式が検出された場合は明示的にエラーを返します。これは設計判断として明示しておくべき事項です。

---

## 4. Compose連携の設計（最終版）

### 4.1 adapter構造

Compose操作は`internal/composeadapter`パッケージに閉じ込めます。このパッケージは以下のインターフェースを公開します。

```go
// internal/composeadapter/adapter.go
package composeadapter

import (
    "context"
    "io"
)

// Adapter はComposeへのアクセスを抽象化する。
// v0ではshell-out実装のみ提供し、将来SDK実装を追加できる構造にする。
type Adapter interface {
    // Version はdocker compose versionの出力を返す。
    // capability probeに使用する。
    Version(ctx context.Context) (VersionInfo, error)

    // Validate は compose config --quiet を実行し、
    // Composeファイルが構文的・意味的に正しいことを確認する。
    Validate(ctx context.Context) error

    // RenderConfigJSON は compose config --format json の結果を返す。
    // サービス一覧やイメージ参照の解決にsource-of-truthとして使う。
    RenderConfigJSON(ctx context.Context) (*RenderedConfig, error)

    // PsJSON は現在動いているコンテナの状態を返す。
    PsJSON(ctx context.Context) ([]ContainerStatus, error)

    // Up は compose up -d --remove-orphans --wait を実行する。
    // progress出力はwriterに書き込む。
    Up(ctx context.Context, opts UpOptions, progress io.Writer) error
}
```

### 4.2 `up --wait`の挙動と対処

レビュー改訂案では「`up -d --wait`を基準にする」としていましたが、**Web検索で発覚した重要な既知バグ**を踏まえて、この方針を修正します。

`docker compose up --wait`を使用する際、init containerがデータを投入して正常終了（exit 0）した後、そのinit containerに依存する他のサービスが存在しない場合、docker composeはexit 1を返すという不具合があります。この問題は2023年から報告されているにもかかわらず、現時点でも継続しています。

c2quayの対処方針は以下です。

**方針1**: `docker compose up -d --remove-orphans --wait`を基本コマンドとする。

**方針2**: exit codeだけでなく、直後に`docker compose ps --format json`を実行し、**実際のコンテナ状態を確認する**。 `--wait`が誤ってexit 1を返した場合でも、psの結果で全サービスが`running`または`exited (0)`なら成功と判定します。

```go
// internal/composeadapter/shell.go (抜粋)
func (a *ShellAdapter) Up(ctx context.Context, opts UpOptions, progress io.Writer) error {
    args := a.buildUpArgs(opts)
    cmd := exec.CommandContext(ctx, "docker", args...)
    cmd.Stdout = progress
    cmd.Stderr = progress
    
    runErr := cmd.Run()
    
    // --wait の既知バグ対応:
    // exit code だけで判断せず、実際のコンテナ状態を確認する。
    statuses, psErr := a.PsJSON(ctx)
    if psErr != nil {
        // ps自体が失敗した場合は、元のrunErrを返す。
        return errors.Join(runErr, fmt.Errorf("ps after up: %w", psErr))
    }
    
    if allServicesHealthy(statuses) {
        // 実体として全サービスが健全なら、runErrは誤判定。
        // ただし警告ログは残す。
        if runErr != nil {
            a.logger.Warn("docker compose up returned non-zero but all services are healthy",
                slog.String("exit_error", runErr.Error()))
        }
        return nil
    }
    
    return runErr
}
```

この「exit codeを鵜呑みにせず、実状態で補正する」という設計は、一見すると冗長に感じられるかもしれません。しかし、**Composeのexit codeが信頼できる前提で組まれたデプロイスクリプトは、実運用で必ず一度は誤判定事故を起こします**。c2quayはこのクラスの事故を構造的に排除するのが目的のツールなので、ここで妥協すべきではありません。

### 4.3 capability probe

`doctor`コマンドと各コマンドの起動時に、Compose環境の能力を確認します。

```go
type VersionInfo struct {
    Version         string // "v2.40.2" など
    SupportsWait    bool   // --wait サポート
    SupportsJSONOut bool   // --format json サポート
    IsHyphenated    bool   // docker-compose形式かどうか（常にtrueならエラー）
}

func (a *ShellAdapter) probeCapabilities(ctx context.Context) (VersionInfo, error) {
    // docker compose version --format json を試す
    // 出力をパースしてバージョンを取得
    // 既知の下限（v2.40.2）を下回る場合は警告
}
```

下限バージョンを下回る場合、`verify`と`deploy`コマンドでは**警告にとどめます**（強制終了はしません）。これはCIやエッジケースでの柔軟性を残すためです。ただし`doctor`では明示的に「推奨バージョンを下回っています」と表示します。

---

## 5. Pact Broker連携の設計（最終版）

### 5.1 HAL駆動クライアントの採用

レビュー改訂案では「endpoint文字列のベタ書きはadapter内部に閉じ込める」としていましたが、これを一歩進めて、**HAL（Hypertext Application Language）を積極的に辿るクライアント設計**を採用します。

Pact Broker APIはHALをハイパーメディア実装として使用しており、プログラマティックなクライアントはリンクを使用してURLを手動で構築すべきではありません。これにより、クライアントを壊すことなくAPIを進化させることができますという公式の指針があります。

具体的には、以下のようなフローになります。

```go
// internal/broker/client.go
type Client struct {
    baseURL    *url.URL
    http       *http.Client
    auth       AuthMethod
    logger     *slog.Logger
    
    // index resource から取得したリンクマップ
    indexLinks map[string]Link
}

type Link struct {
    Href      string
    Templated bool // URL templateの場合
}

// Start はindex resourceを取得し、以降のAPI呼び出しで使う
// _linksマップを構築する。起動時に一度だけ呼ばれる。
func (c *Client) Start(ctx context.Context) error {
    resp, err := c.get(ctx, c.baseURL.String())
    if err != nil {
        return fmt.Errorf("fetch broker index: %w", err)
    }
    
    var index struct {
        Links map[string]Link `json:"_links"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
        return fmt.Errorf("decode index: %w", err)
    }
    c.indexLinks = index.Links
    return nil
}

// link は relation name からURLを解決する。
// 見つからない場合は error を返す（固定URLへのfallbackはしない）。
func (c *Client) link(rel string) (string, error) {
    l, ok := c.indexLinks[rel]
    if !ok {
        return "", fmt.Errorf("broker does not expose relation %q; check broker version", rel)
    }
    return l.Href, nil
}
```

このアプローチの利点は、**c2quayが対応していないブローカーバージョンに遭遇したときに、明確なエラーメッセージを返せること**です。「URL決め打ちで叩いて404が返ってきた」ではなく「このブローカーは`pb:can-i-deploy`リレーションを公開していません」と具体的に伝えられます。

### 5.2 対応ブローカーバージョン

c2quayは以下を対応対象とします。

- **OSS Pact Broker v2.113.0以上**: v2.113.0でaggregated de-duplicated list of provider statesエンドポイントが追加されたように、近年の機能追加があるため、この付近を下限とするのが妥当です。
- **PactFlow**: 継続的に更新されるSaaSなので、最新版前提で動作確認します。

対応していないバージョンに対しては、起動時の`Start()`でindex resourceを取得した時点で、必要なリレーション（`pb:environments`, `pb:can-i-deploy`, `pb:pacticipant`など）の有無を確認し、不足があればわかりやすいエラーを返します。

### 5.3 認証方式

Pact Broker CLIと整合するため、以下の環境変数を優先的に使用します。

- `PACT_BROKER_BASE_URL`: ブローカーのベースURL
- `PACT_BROKER_USERNAME`, `PACT_BROKER_PASSWORD`: Basic認証
- `PACT_BROKER_TOKEN`: Bearer認証

設定ファイルには`broker.base_url`を書けますが、実値は環境変数が優先されます。認証情報は**絶対にYAMLに書かせません**。

### 5.4 `record-deployment`の順序

レビュー改訂案の通り、以下の順序を厳守します。

1. gate check
2. compose up & wait
3. smoke（設定されていれば）
4. record-deployment

pact-broker record-deploymentコマンドは、デプロイプロセスの最後に、失敗の可能性がなく、前バージョンのインスタンスがもう動いていない時点で呼ばれるべきです。これが呼ばれるとすぐに、そのアプリケーション/環境の以前にデプロイされたバージョンが自動的に「デプロイ解除済み」としてマークされます。

この仕様を鑑みると、**record-deploymentを先に呼ぶことは絶対に避けなければなりません**。実デプロイが失敗したにもかかわらずBroker上では「新バージョンがデプロイ済み」と記録されると、次回以降の`can-i-deploy`が誤った判定をする可能性があるためです。

---

## 6. バージョニング戦略（最終版）

### 6.1 採用する戦略

**推奨: `manifest_file`**

CIあるいはbuild stepが生成した不変manifestを読みます。例:

```json
{
  "services": {
    "api": {
      "version": "2026-04-17-a1b2c3d",
      "image": "ghcr.io/example/api@sha256:abc123..."
    },
    "web": {
      "version": "2026-04-17-a1b2c3d",
      "image": "ghcr.io/example/web@sha256:def456..."
    }
  }
}
```

この方式が推奨される理由は、**CIが決定した「このビルド」を明示的に固定できる**ことです。ローカル検証や`HEAD`からの即時デプロイではなく、「CIで緑になったあのビルド」をデプロイするのが本番運用の基本形なので、それに合った設計と言えます。

**次善: `resolved_image_digest`**

`docker compose config --format json`の結果からimage参照を取り出し、digest付き参照（`image@sha256:...`）であればそれを使用します。tagのみの参照だった場合はエラーを返します。

**条件付き: `git_sha`**

モノレポで全サービスが1コミットに束ねられる場合に有効です。`git rev-parse HEAD`の結果を全サービスの共通バージョンとします。

### 6.2 非推奨戦略

**`image_tag`**: v0では実装を見送ります。mutable tagを踏むリスクが利便性を上回るためです。

---

## 7. 設定ファイルスキーマ（最終版）

```yaml
compose:
  files:
    - compose.yaml
  project_name: myapp

broker:
  # 実値は環境変数 PACT_BROKER_BASE_URL 優先
  base_url: https://pact-broker.example.com

versioning:
  strategy: manifest_file
  options:
    path: .c2quay/versions.json

deploy:
  wait: true
  wait_timeout: 180s
  smoke:
    command: ./scripts/smoke.sh
    timeout: 30s
    env:
      TARGET_ENV: production

environments:
  production:
    all_or_nothing: true
    services:
      api:
        pacticipant: api
      web:
        pacticipant: web
```

改訂案との差分を説明します。

`broker`は改訂案で採用された命名です。`pact_broker`より簡潔で、内部パッケージ名との衝突も回避できます。

`deploy.wait_timeout`を追加しました。`--wait`のデフォルトタイムアウトはComposeでは100年近い巨大な値になっていますが、c2quayでは現実的なデフォルト（180秒）を持たせます。

`smoke.env`を追加しました。スモークテストに必要な環境変数を設定できるようにしています。`TARGET_ENV`のように、スクリプト側で環境名を参照する使い方を想定しています。

`all_or_nothing: true`を環境設定レベルで明示します。将来的に部分ロールアウトをサポートする際は、これを`false`に変更する選択肢を残します。

---

## 8. プロジェクト構造（最終版）

```text
c2quay/
├── cmd/
│   └── c2quay/
│       └── main.go
├── internal/
│   ├── cli/
│   │   ├── root.go
│   │   ├── init.go
│   │   ├── doctor.go
│   │   ├── verify.go
│   │   ├── deploy.go
│   │   ├── status.go
│   │   └── version.go
│   ├── config/
│   │   ├── config.go
│   │   ├── validate.go
│   │   └── testdata/
│   ├── broker/
│   │   ├── client.go         # HAL driven client
│   │   ├── links.go          # relation resolver
│   │   ├── auth.go           # auth methods
│   │   ├── canideploy.go
│   │   ├── deployment.go
│   │   ├── environment.go
│   │   └── errors.go
│   ├── composeadapter/
│   │   ├── adapter.go        # interface
│   │   ├── shell.go          # shell-out implementation
│   │   ├── capabilities.go
│   │   ├── configjson.go     # config --format json parser
│   │   └── psjson.go
│   ├── versioning/
│   │   ├── strategy.go
│   │   ├── manifest_file.go
│   │   ├── resolved_digest.go
│   │   └── git_sha.go
│   ├── release/
│   │   ├── gate.go
│   │   ├── pipeline.go
│   │   ├── smoke.go
│   │   ├── snapshot.go
│   │   └── diff.go
│   ├── lock/
│   │   └── file_lock.go      # 環境ロック実装
│   ├── logging/
│   │   └── setup.go          # slog初期化
│   ├── output/
│   │   ├── text.go
│   │   └── json.go
│   └── doctor/
│       ├── checks.go
│       └── report.go
├── test/
│   ├── integration/          # Testcontainers使用
│   └── e2e/                  # 実Compose project
├── docs/
│   ├── architecture.md
│   └── adr/
├── .github/
│   └── workflows/
│       ├── ci.yml
│       └── release.yml
├── go.mod
├── go.sum
├── Makefile
├── README.md
└── LICENSE
```

---

## 9. 実装フェーズ（最終版）

改訂案のフェーズ分けは妥当ですが、各フェーズの成果物をより具体化します。

### フェーズ0: 骨格とdoctor（1週間）

**成果物**

- Cobraルートコマンドとサブコマンドスタブ
- 設定ファイルロード
- `slog`ベースのロギング初期化
- `c2quay doctor`の最小実装（Docker daemon到達、Compose version検出）
- CI/lint/test/GoReleaserの設定

**ポイント**

`doctor`を最初に実装するのは、**以降のフェーズで開発しやすくするため**です。開発者自身が自分の環境を確認するコマンドが最初に動くと、その後の統合テストやE2Eテストのデバッグが格段に楽になります。

### フェーズ1: Compose adapter（1週間）

**成果物**

- `ShellAdapter`の全メソッド実装
- `docker compose version --format json`のパース
- `docker compose config --format json`のパース
- `docker compose ps --format json`のパース
- ハイフン形式の拒否ロジック
- `--wait`誤判定対策（ps確認）

**ポイント**

このフェーズのテストは**実環境のdocker composeに依存します**。CIでは`services: - docker:dind`のような構成でDockerを立ち上げる必要があります。Testcontainers-Goも併用候補です。

### フェーズ2: Versioning（1週間）

**成果物**

- `Strategy`インターフェース
- `manifest_file`実装
- `resolved_image_digest`実装
- `git_sha`実装
- 各戦略のユニットテスト

**ポイント**

`resolved_image_digest`戦略は、**フェーズ1のComposeアダプターに依存します**。`config --format json`の結果から各サービスのイメージ参照を抽出し、digest形式であればそれを返し、tag形式ならエラーにします。

### フェーズ3: Broker adapter（2週間）

**成果物**

- HAL driven HTTPクライアント
- 認証（Basic/Bearer）
- index resource取得とリレーション解決
- `create-environment`の存在確認
- `can-i-deploy`
- `record-deployment`
- 統合テスト（Testcontainersで`pactfoundation/pact-broker`を起動）

**ポイント**

このフェーズが最も時間を要します。HAL駆動のクライアント設計は一見複雑ですが、**一度組んでしまえばBrokerのAPI進化に追従できる**資産になります。Pact Brokerは過去にdeprecation（例: `pacts`リレーションを`pb:pacts`に置換）を経験しており、HAL relationによる間接参照が威力を発揮する場面が実際にあります。

### フェーズ4: verify（1週間）

**成果物**

- 複数サービスの並列gate check（並列度4）
- text出力（人間可読）
- JSON出力（CI連携）
- 終了コード（0=pass, 1=gate failed, 2=operator error）
- `--service`フラグによる絞り込み

**ポイント**

`verify`が動いた時点で**v0.1.0を出せる**状態になります。このマイルストーンは重要で、GitHubに公開してコミュニティの反応を見るタイミングとしても有効です。

### フェーズ5: deploy/status（2週間）

**成果物**

- 環境ロック機構（ファイルベース）
- pre/postスナップショット採取
- `docker compose up -d --remove-orphans --wait`実行
- optional smoke実行
- `record-deployment`
- `status`コマンド（Broker記録 vs ローカル実態の差分表示）
- `--dry-run`フラグ

**ポイント**

環境ロックは重要です。複数の開発者やCIパイプラインが同時に`c2quay deploy --env production`を実行するのを防ぎます。v0ではファイルベース（`.c2quay/locks/production.lock`）の単純な実装で十分ですが、将来的にはリモートロック（Consul, etcd）の選択肢を残します。

### フェーズ6: Hardening（1〜2週間）

**成果物**

- 統合テストとE2Eテストの網羅
- 失敗パステスト（Brokerダウン、compose失敗など）
- rollback hintの生成
- ドキュメント整備（README、使い方ガイド、FAQ）
- GoReleaserでのマルチプラットフォームビルド
- Homebrew tapの準備

**ポイント**

このフェーズで`testing/synctest`を活用します。testing/synctestパッケージは1.24で実験的に導入され、1.25で安定版として使用可能になりました。ただしRun関数は非推奨になり、代わりにTestを使用すべきです。c2quayではスモークのタイムアウトや`--wait`の待機時間など時間依存のロジックがあるため、仮想時間でテストできる`synctest`は有用です。

### リリースマイルストーン

- **v0.1.0**: フェーズ4完了時点。`verify`のみ動作。
- **v0.2.0**: フェーズ5完了時点。`deploy`と`status`が動作。
- **v0.3.0**: フェーズ6完了時点。rollback hintとhardening。
- **v1.0.0**: ユーザー1社以上で本番運用実績が出た段階。

---

## 10. 観測可能性

改訂案では触れられていなかった観点ですが、c2quayのような**本番デプロイをゲートするツール**では、事後のトラブルシュートに使える情報を確実に残すことが非常に重要です。

### 10.1 構造化ログ

すべての主要操作は`slog`で構造化ログを出力します。Go 1.25で追加されたlog/slogの新機能であるGroupAttrsにより属性をグループ化でき、Record.Sourceで呼び出し元情報を記録できます。これを使って以下のようなログ構造を作ります。

```go
logger.Info("gate check completed",
    slog.String("env", "production"),
    slog.GroupAttrs("gate",
        slog.Int("services_checked", 5),
        slog.Int("services_passed", 5),
        slog.Duration("duration", checkDuration),
    ),
    slog.GroupAttrs("broker",
        slog.String("url", brokerURL),
        slog.Int("api_calls", apiCallCount),
    ),
)
```

### 10.2 監査ログ

`--audit-log`フラグで指定されたファイルに、JSON Linesフォーマットで操作履歴を追記します。これは以下を含みます。

- 実行日時
- 実行ユーザー（`USER`環境変数）
- 実行ホスト名
- コマンドライン引数
- 各ステップの結果と所要時間
- Broker API呼び出しの詳細

本番運用では、このログをログ集約基盤（Splunk、CloudWatch Logsなど）に転送することで、「いつ誰がどのデプロイをゲートしたか」を監査できます。

### 10.3 rollback hint

デプロイが失敗した場合、次のような情報を標準エラーに出力します。

```text
Deployment failed. Rollback hints:

Environment: production
Failed at step: smoke
Affected services:
  - api: was v2026-04-16-abc123, attempted v2026-04-17-def456
  - web: unchanged (v2026-04-16-xyz789)

Suggested rollback command:
  c2quay rollback --env production --to-snapshot .c2quay/snapshots/2026-04-17T14-30-52.json

Or manually:
  docker compose -f compose.yaml -p myapp up -d api=ghcr.io/example/api@sha256:abc123...

Note: record-deployment was NOT called for the failed attempt.
The broker still records v2026-04-16-abc123 as the deployed version of api.
```

この「失敗時に何をすべきか」を明示する設計は、深夜のインシデント対応で圧倒的に価値を発揮します。

---

## 11. テスト戦略（最終版）

### 11.1 ユニットテスト

各パッケージの関数を独立してテストします。カバレッジ目標は80%以上。

特に以下は重点的にテストします。

- **config/validate.go**: 様々な不正YAMLに対するエラーメッセージの正確さ
- **broker/links.go**: HAL relationの解決ロジック
- **broker/canideploy.go**: httptest.Serverを使った全レスポンスケース
- **composeadapter/shell.go**: モックexec経由でのコマンド生成確認
- **versioning/*.go**: 各戦略の境界ケース

### 11.2 統合テスト

Testcontainersで`pactfoundation/pact-broker`コンテナを起動し、実ブローカーに対してc2quayの全コマンドを実行します。

- 認証あり/なし
- environment存在あり/なし
- can-i-deploy pass/fail
- record-deployment成功/失敗
- ネットワーク障害時の挙動

### 11.3 E2Eテスト

シンプルなCompose project（2サービス + 明示的Pact依存）を`test/e2e/fixtures/`に用意し、以下のシナリオを通します。

- `doctor`: 環境チェック成功
- `verify`: 合格ケース
- `verify`: 不合格ケース
- `deploy`: 全成功
- `deploy`: `--wait`誤判定ケース（init container）
- `deploy`: スモーク失敗
- `deploy`: record-deployment失敗
- `status`: Broker記録とローカル実態の差分検出

### 11.4 時間依存テスト

`testing/synctest`を使って、タイムアウト関連のロジックを実時間なしでテストします。

```go
func TestSmokeTimeout(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        
        // 35秒かかるスモークを模擬
        slowSmoke := func(ctx context.Context) error {
            time.Sleep(35 * time.Second) // synctestで即座に進む
            return nil
        }
        
        err := runSmoke(ctx, slowSmoke)
        assert.ErrorIs(t, err, context.DeadlineExceeded)
    })
}
```

### 11.5 contract test

将来的にc2quay自身のBroker adapterの振る舞いをPact fixtureで固定しておくと、Brokerの仕様変更で壊れにくくなります。これはv0.3.0以降の検討事項とします。

---

## 12. Open Issues（最終版）

### 12.1 部分ロールアウト

初期版は**all-or-nothing**で正しいです。将来的に`environments.production.all_or_nothing: false`で有効化できる構造にしておきます。ただし、部分デプロイのsemanticsは慎重な設計が必要で、v1.0以降の検討事項とします。

### 12.2 自動ロールバック

初期版では**不要**です。ただし**rollback hintの生成**はフェーズ6で必ず入れます。何も情報が出ない失敗が、運用上最も危険だからです。

### 12.3 SSH実行

**延期で正しい**です。将来実装する場合は、`golang.org/x/crypto/ssh`ではなく、`ssh`バイナリへのサブプロセス委譲を優先します。理由はユーザーの`~/.ssh/config`がそのまま有効になる、多要素認証や踏み台経由に対応しやすい、という実用面での利点があるためです。

### 12.4 Compose SDK adapter

**「今は使わないが、採用余地は残す」が正解**です。SDKが安定し、`docker/cli`への依存がより緩やかになった時点で再評価します。

### 12.5 OCIアーティファクトCompose

Compose v2.40.2以降では、`include`でOCIアーティファクトを参照する際のパストラバーサル脆弱性（CVE-2025-62725）が修正されています。c2quayはこの機能をネイティブサポートする必要はありませんが、Composeが勝手に解決してくれるので、`docker compose config --format json`の結果を素直に受け取るだけで問題ありません。この点は改めて「CLI委譲こそ正解」という判断の追い風になります。

### 12.6 環境ロックのスコープ

ファイルベースの単純な実装では、マルチホストでの排他は不可能です。「デプロイホストが1台」または「CIが1本だけ実行される」前提で十分機能しますが、複数のCI worker、複数の踏み台、という状況ではリモートロック（Redis、Consul）が必要になります。v0では単純実装で出し、需要が出てから拡張します。

---

## 13. 最終判断

改訂案の骨格は正しく、最終版でも維持します。Web検索による追加の検証を踏まえて、以下を確定させました。

**維持する骨格**

- c2quayはゲートであってデプロイツールではない
- Compose解決はCLI source-of-truth
- 不変のrelease identityを中心に据える
- `up --wait`を基準にする
- Broker capability probeを入れる
- atomicではなくrestart-safe / auditable
- Compose SDK adapterは将来拡張ポイントとして残す
- Local-only v0
- Direct Broker HTTP
- record-deployment after successful deploy
- verify-first release flow

**Web検索で判明し、最終版で明示的に組み込んだ追加判断**

- Docker Compose CLI下限をv2.40.2に固定（CVE-2025-62725対応）
- `docker-compose`ハイフン形式のサポートを明示的に拒否
- `up --wait`の既知バグに対するpsクロスチェック
- HAL driven Broker client（relationベースのURL解決）
- Go 1.26.2を開発基準、Go 1.25をサポート下限（公式サポート最新2リリースに準拠）
- `testing/synctest`の明示採用（Go 1.25で安定化）
- `slog.GroupAttrs`での構造化ログ
- Pact-Goとの共存互換性（Go 1.23以上）

この最終版なら、c2quayは「Composeの上に雑に1枚ラップしたツール」ではなく、**現在のCompose/Pactエコシステムの実態に即した、壊れにくい単一バイナリOSS**として成立します。実装に着手できる状態です。

---

## 参考資料

### Docker Compose

- Docker Compose release notes: https://github.com/docker/compose/releases
- Docker Compose history (v1→v5): https://docs.docker.com/compose/intro/history/
- Compose SDK: https://docs.docker.com/compose/compose-sdk/
- `docker compose up` CLI reference: https://docs.docker.com/reference/cli/docker/compose/up/
- `docker compose config` CLI reference: https://docs.docker.com/reference/cli/docker/compose/config/
- `docker compose ps` CLI reference: https://docs.docker.com/reference/cli/docker/compose/ps/
- Issue #10596 (`up --wait` exits 1 with init containers): https://github.com/docker/compose/issues/10596
- CVE-2025-62725 writeup: https://bdteo.com/docker-compose-major-changes-since-october-2023/

### Pact

- Pact Docs: Can I Deploy: https://docs.pact.io/pact_broker/can_i_deploy
- Pact Docs: Recording deployments and releases: https://docs.pact.io/pact_broker/recording_deployments_and_releases
- Pact Docs: HAL Browser: https://docs.pact.io/pact_broker/administration
- Pact Docs: API documentation: https://docs.pact.io/pact_broker/advanced_topics/api_docs
- Pact Broker CLI README: https://docs.pact.io/pact_broker/client_cli/readme
- Pact Open Source Update (May 2025): https://docs.pact.io/blog/2025/05/28/pact-open-source-update-may-2025
- Pact-Go v2: https://pkg.go.dev/github.com/pact-foundation/pact-go/v2

### Go

- Go 1.26 Release Notes: https://go.dev/doc/go1.26
- Go 1.26 release announcement: https://go.dev/blog/go1.26
- Go 1.25 Release Notes (testing/synctest, log/slog GroupAttrs): https://go.dev/doc/go1.25
- Go release history: https://go.dev/doc/devel/release
- Go 1.26 interactive tour: https://antonz.org/go-1-26/
