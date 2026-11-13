> **V8 LTS:** `main` は V8 の保守対象ブランチです。V8 設定形式と対応する V8 Panel を使用してください。設定パス、維持される v0 API、旧 Panel の書き込み保護、ロールバック要件は [LTS V8 互換性](docs/lts/v8-development-compatibility.md) を参照してください。正式版は [Releases](https://github.com/BlueSkyXN/CPA-Core-LTS/releases/latest) から取得できます。

# CPA Core LTS

[English](README.md) | [中文](README_CN.md) | 日本語

CPA Core LTS は `router-for-me/CLIProxyAPI` の長期メンテナンス fork です。

- LTS ベースライン: `v6.9.49`
- ベースライン commit: `b8bba053fcdafd80abc2152c88c78f4e7713c05a`
- Upstream source: <https://github.com/router-for-me/CLIProxyAPI>
- LTS repository: <https://github.com/BlueSkyXN/CPA-Core-LTS>
- Companion Web UI: <https://github.com/BlueSkyXN/CPA-Panel-LTS>
- Default panel release source: `https://github.com/BlueSkyXN/CPA-Panel-LTS`

## メンテナンスモデル

この fork は、選択したベースライン以降の upstream が完全な usage statistics フローを変更または削除したため存在します。本リポジトリの目的は、CLI proxy core をメンテナンス可能な状態に保ちながら、`v6.9.49` 時代の統計機能を保持することです。

メンテナンスルール:

- `main` は CPA-Core-LTS の唯一の製品ラインです。通常メンテナンス用に別の「statistics branch」は作りません。
- CPA-Core-LTS は operator-driven な protected full-sync merge PR によって `router-for-me/CLIProxyAPI` を追跡します。通常はメンテナーが依頼したときに AI agent が同期作業を準備します。
- usage collection、`internal/usage/`、`/v0/management/usage`、`/v0/management/usage/export`、`/v0/management/usage/import`、`usage-statistics-enabled` を保持します。
- 通常の upstream 変更はデフォルトで取り込みます。upstream 変更が protected delta と衝突する場合は、LTS 契約を削除または弱体化させるのではなく、upstream 変更を適応させます。
- GitHub Sync fork、ファイル単位の上書き、`main` 上での直接 `git pull upstream main` は使用しません。
- このリポジトリでは upstream sync を自動スケジュールしません。ここに記載するのは、人間または AI が操作するための再現可能な手順です。
- 予定された軽量化では宣伝文、未使用機能、非 LTS release machinery を削除できますが、統計契約や `CPA-Panel-LTS` 互換性を弱めてはいけません。

この core service は、対応する management panel usage statistics UI を保持する `CPA-Panel-LTS` と組み合わせて使用することを想定しています。デフォルトでは、`/management.html` は最新の `CPA-Panel-LTS` release asset `management.html` からダウンロードされます。

## 元プロジェクト概要

CLI向けのOpenAI/Gemini/Claude/Codex/Grok互換APIインターフェースを提供するプロキシサーバーです。

OAuth経由でOpenAI Codex（GPTモデル）およびClaude Codeもサポートしています。

ローカルまたはマルチアカウントのCLIアクセスを、OpenAI（Responses含む）/Gemini/Claude互換のクライアントやSDKで利用できます。

## 概要

- CLIモデル向けのOpenAI/Gemini/Claude/Grok互換APIエンドポイント
- OAuthログインによるOpenAI Codexサポート（GPTモデル）
- OAuthログインによるClaude Codeサポート
- OAuthログインによるGrok Buildサポート
- プロバイダールーティングによるAmp CLIおよびIDE拡張機能のサポート
- ストリーミング、非ストリーミング、および対応環境でのWebSocketレスポンス
- 関数呼び出し/ツールのサポート
- マルチモーダル入力サポート（テキストと画像）
- ラウンドロビン負荷分散による複数アカウント対応（Gemini、OpenAI、Claude、Grok）
- シンプルなCLI認証フロー（Gemini、OpenAI、Claude、Grok）
- Generative Language APIキーのサポート
- AI Studioビルドのマルチアカウント負荷分散
- Claude Codeのマルチアカウント負荷分散
- OpenAI Codexのマルチアカウント負荷分散
- Grok Buildのマルチアカウント負荷分散
- 設定によるOpenAI互換アップストリームプロバイダー（例：OpenRouter）
- プロキシ埋め込み用の再利用可能なGo SDK（`docs/sdk-usage.md`を参照）

## はじめに

### 任意のローカル流量制御（Flow V3）

呼び出し元、モデル、個別の上流 Auth レコードに対する同時実行数とスライディングウィンドウ制限、有界キュー、オンデマンド監視を提供します。実行制御、リアルタイム更新、リソース採取は既定で無効です。新しい空ポリシーで version を省略した場合は schema 3 として解釈されます。既存の非空で version のないポリシーは旧セマンティクスを維持し legacy-policy として表示され、明示的な version 1/2 も旧セマンティクスを維持します。Home、上流 quota、完全な usage 統計を置き換えません。設定適用に失敗した場合は最後の有効なポリシーを維持します。対応バージョン、移行、ロールバックは [Flow ガイド](docs/lts/flow-control.md) を参照してください。

CLIProxyAPIガイド：[https://help.router-for.me/](https://help.router-for.me/)

## 管理API

[MANAGEMENT_API.md](https://help.router-for.me/management/api)を参照

## 使用量統計

上流の CLIProxyAPI v6.10.0 以降では、組み込み使用量統計が core project から分離されました。CPA-Core-LTS は CPA-Panel-LTS 互換性のため、組み込みの `/v0/management/usage` 統計と export/import API を保持し、外部ダッシュボードが同じイベントを利用できるよう Redis 互換の使用量キューも保持します。

### [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper)

CLIProxyAPI向けの独立した使用量永続化・可視化サービス。CLIProxyAPIデータを定期同期してSQLiteに保存し、集計APIと、使用量や各種統計を確認できる組み込みダッシュボードを提供します。

### [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)

CLIProxyAPI 向けのセルフホスト型管理コンソールと可観測性ダッシュボードです。追加サービス不要の軽量パネルとしても、リクエスト履歴の永続化と分析機能を備えた Manager Server としても利用できます。OAuth 認証情報、AI プロバイダー、クライアント API キー、モデルエイリアス、プラグイン、CPA 設定を一元管理します。SQLite に保存したリクエスト履歴から、リアルタイム監視、エラー原因の調査、モデル・プロバイダー・アカウント・キー・チャネル別の Token 使用量、レイテンシ、推定コストを分析できます。モデル料金の編集とワンクリック同期に対応し、Codex と xAI のクォータ確認、リセット情報、アカウントの健全性チェック、制御された復旧操作を提供します。Docker またはクロスプラットフォームのネイティブパッケージで導入でき、データはローカルに保存されます。

### [Oh-My-CPA](https://github.com/WizisCool/oh-my-cpa)

Ant Design を採用した CLIProxyAPI v8+ 向けのモダンな管理コンソール。CPAMC に相当する主要な管理機能と、SQLite に永続化するリクエスト記録・使用量分析を統合し、OAuth アカウント、API プロバイダー、クライアントキー、クォータ、料金設定、CPA 運用を一つの画面で管理できます。リクエストごとのレイテンシ、最初のトークンまでの時間（TTFT）、トークン数、コストを追跡し、多条件フィルター、リアルタイムダッシュボード、トークンヒートマップを提供します。OpenRouter の価格同期、カスタム料金、リクエストごとの価格スナップショットで過去のコストを保持し、組み込み Agent と MCP ツールで分析と日常管理を支援します。

## SDKドキュメント

- 使い方：[docs/sdk-usage.md](docs/sdk-usage.md)
- 上級（エグゼキューターとトランスレーター）：[docs/sdk-advanced.md](docs/sdk-advanced.md)
- アクセス：[docs/sdk-access.md](docs/sdk-access.md)
- ウォッチャー：[docs/sdk-watcher.md](docs/sdk-watcher.md)
- カスタムプロバイダーの例：`examples/custom-provider`

## コントリビューション

コントリビューションを歓迎します！お気軽にPull Requestを送ってください。

1. リポジトリをフォーク
2. フィーチャーブランチを作成（`git checkout -b feature/amazing-feature`）
3. 変更をコミット（`git commit -m 'Add some amazing feature'`）
4. ブランチにプッシュ（`git push origin feature/amazing-feature`）
5. Pull Requestを作成

## 関連プロジェクト

CLIProxyAPI をベースにしたプロジェクトのエコシステム一覧は上流リポジトリで管理されています：<https://github.com/router-for-me/CLIProxyAPI#who-is-with-us>

## その他の選択肢

以下のプロジェクトはCLIProxyAPIの移植版またはそれに触発されたものです：

### [9Router](https://github.com/decolua/9router)

CLIProxyAPIに触発されたNext.js実装。インストールと使用が簡単で、フォーマット変換（OpenAI/Claude/Gemini/Ollama）、自動フォールバック付きコンボシステム、指数バックオフ付きマルチアカウント管理、Next.js Webダッシュボード、CLIツール（Cursor、Claude Code、Cline、RooCode）のサポートをゼロから構築 - APIキー不要

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

コーディングを止めない。無料および低コストのAIモデルへのスマートルーティングと自動フォールバック。

OmniRouteはマルチプロバイダーLLM向けのAIゲートウェイです：スマートルーティング、負荷分散、リトライ、フォールバックを備えたOpenAI互換エンドポイント。ポリシー、レート制限、キャッシュ、可観測性を追加して、信頼性が高くコストを意識した推論を実現します。

### [Playful Proxy API Panel (PPAP)](https://github.com/daishuge/playful-proxy-api-panel)

上流に近い使い方を維持する公開CLIProxyAPI互換フォーク兼管理パネルです。内蔵の使用量統計を復元し、キャッシュヒット率、初回バイト待ち時間、TPSの記録、Docker向けのセルフホスト手順を追加しています。

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

Tauri 2 + Vue 3で構築された、複数のOpenAI Codexデスクトップアカウントを管理するためのツールです。保存済みのChatGPT/Codex認証プロファイルを切り替え、5時間および週次クォータ使用量をリアルタイムで確認し、tokenの状態を検証し、現在のアカウント詳細を表示し、手動コピーなしでauth.jsonファイルをインポートまたは保存できます。

### [Alex](https://github.com/madhavajay/alex)

CLIProxyAPIに着想を得た、オプションUI付きのローカルRust LLMプロキシ。コーディングエージェントを複数プロバイダーへルーティングし、ローカルトレース、スクリプト可能なミドルウェア、複数サブスクリプションの統合、フェイルオーバー、インスタントメッセンジャー経由の再認証に対応します。
### [cliproxy-rs](https://github.com/vayungodara/cliproxy-rs)

CLIProxyAPIのRust移植版です。同じ`config.yaml`と認証ファイルを読み込み、同じルートとv8 Management APIを提供するため、両者の間をどちらの方向にも切り替えられます。管理ダッシュボード（アカウントごとの5時間・週次クォータ表示、アカウントのサインイン、クライアント設定ガイド）を内蔵した単一バイナリで、週次ウィンドウが最も早くリセットされるアカウントから使うオプトインの`soonest-reset`ルーティング戦略も追加しています。

> [!NOTE]
> CLIProxyAPIの移植版またはそれに触発されたプロジェクトを開発した場合は、PRを送ってこのリストに追加してください。

## ライセンス

本プロジェクトはMITライセンスの下でライセンスされています - 詳細は[LICENSE](LICENSE)ファイルを参照してください。
