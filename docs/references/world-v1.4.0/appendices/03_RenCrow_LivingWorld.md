# 付録03　RenCrowの生活世界とWORLD接続仕様

**統合版：WORLD v1.4.0／整理日：2026-10-20**  
**優先資料：2026-10-19 `RenCrow_WORLD_CORE_HOUSE_PORTAL_FINAL_v1.0`**

HOUSEを共通WORLDで実現する仕様として、10月19日版の合意事項を優先し、旧LivingWorldの報告・日記・物語投影を接続する。設計、同梱監査によるソース確認、本番受入を区別し、不足契約の具体化は「今回補完」と記す。原資料の実装開始指示や外部コード変更・配備は今回実行しない。[R00] [R12]

## 1. 実現する生活世界

HOUSEの本番World IDは `rencrow-home`。常住者はMio、Shiro、Kuro、Midori、Aka、Ao、Kin、Ginの8体。Luminaは通常不在のVisitorで、許可された訪問中だけ存在する。最大9体の表示と、9体の常住は区別する。訪問Presenceと本人の会話・音声接続も別条件である。[R02] [R05]

WORLDはPORTALを閉じても生活を進め、CORE停止中も保存済みの許可範囲で非認知生活を継続する。調査、創作、会話、実仕事、経験の記憶はCOREが扱い、動作と認知成果を根拠付きで対応させる。[R02] [R03]

共通Kernelを再利用しても、異世界技術者、GAMES、HOUSEのInstance、State、Event履歴、Canon、設定は分離する。**今回補完：** 8体の制約はHOUSE用Profileへ置き、汎用Kernelに固定しない。物語のCharacterとCOREのAgentIdentityも別の識別対象として紐付ける。

## 2. 正本と責務

| 対象 | 正本・実行Owner | 他の領域へ渡すもの |
|---|---|---|
| Agent identity、Persona、関心、Goals、Autonomy、認知としての関係性、Memory | CORE | 許可済み活動ID、意図、必要な認可済み投影 |
| Task、Run、Tool、Harness、会話Session、発話原文、認知成果 | COREと各正規実行Owner | 仕事集合、確定発話参照、結果receipt |
| 世界時刻、位置、移動、非認知活動、設備占有、訪問Presence、Event、Snapshot | WORLD | 観測候補、Snapshot、順序付きEvent、Viewer用状態 |
| RoomTopology、家具、Anchor、Collision、Nav、空間アクセス | HOUSE SpatialBundle。読込・実行はWORLD | 版とhashを付けた表示用空間データ |
| Renderer、画像・モデル・Clip、Camera、PinP、吹き出し・音声UI | PORTAL | 表示結果。世界状態への書戻しはしない |
| 個人・家族の予定と通知 | ASSISTANT | HOUSE生活とは別の契約 |

HOUSEは製品コンテンツであり、独立daemonや人格・Task・MemoryのStoreを追加しない。新しい `RenCrow_AGENT` も作らずCOREの既存機能を再利用する。WORLDは既存R05から独立Goサービスへ拡張する技術案を採用候補とする。[R02] [R04]

## 3. 生活・認知・仕事表示・描画を分ける

| 状態 | Owner | 意味 |
|---|---|---|
| CognitiveState | CORE | 本人の認知処理、仕事、待機、完了・失敗とreceipt |
| LifeState | WORLD | selecting、moving、performing、idle、resting、sleeping等 |
| WorkPresence／WorkPresentation | WORLD | CORE投影から導く仕事場への移動、仕事姿勢、待機、状態不明 |
| VisualState | PORTAL | sprite、clip、向き、補間、camera、lipsync |

一体のprimary Activityは最大一つ。仕事表示は別投影として持つ。読書姿勢はWORLDで成立するが、本の内容を分析したという事実にはCOREの結果が必要である。姿勢、時間経過、PC前への到着から実仕事の成功を作らない。[R02] [R03]

通常Chatはその場で行い、実仕事の開始時だけ分類に応じて1Fへ移動する。COREの仕事は移動・着席を待たない。移動中に仕事が終わっても経路・占有・予約解除はWORLDで処理する。[R03] [R04]

## 4. 常時生活と認知処理の実行

WORLDのtickはClockを注入可能な状態遷移とし、HTTP、LLM、Task、Toolを同期呼び出ししない。活動、経路、占有、仕事投影、訪問等の意味Eventを保存し、歩行フレームはPORTALが開始時刻・経路・速度から補間する。[R03]

COREは人格とAutonomyから `LifePolicyProjection` を機械的に生成する。Actor、版、許可活動ID、条件、期限を渡し、人格本文、好みの複製、Memory、モデル・GPU情報は含めない。WORLDは歩行、着席、運動、衣装・読書・睡眠の姿勢、休息、四神の日常動作等の許可済み非認知活動だけを選ぶ。[R03] [R04]

Mio・Shiro・Midoriの調査・創作はCOREが資源と仕事状況を検査して開始する。Kuroの本格仕事中は生活LLMを0にする。四神は平時はペットとして生活し、依頼なしのCoder推論を0にする。[R04] [R10]

CORE不通時は保存済みの許可に基づく保守的なidle/rest/sleep等へ限定し、新しい知的活動、他者との発話、知的達成Eventを作らない。LifePolicyの期限切れ後に許すidle/restもPolicyで明示する。WORLD停止後は限定的な一回のcatch-upを行い、結果を `recovery_catchup` として残す。停止中の会話、調査、理解、記憶を遡って生成しない。[R03] [R07]

tick約1Hz、Encounter cooldown300秒、256意味Eventまたは60秒ごとのSnapshotは初期値候補であり、実証済み性能値ではない。PORTAL非表示でもEventが蓄積されていれば記録欠落ではない。

## 5. COREとWORLDの接続

| 方向 | 内容 |
|---|---|
| CORE → WORLD：VersionedWrite | LifePolicy、FullActorWorkSet、ActivityIntent、確定UtteranceLink、Visitor admission/departure |
| CORE → WORLD：Read／Stream | Snapshot、順序付きEvent、EncounterOpportunity、ObservationCandidateの取得 |
| PORTAL → CORE：Readのみ | 認可済みの世界Snapshot、Event、SSEと表示用投影 |

WORLDからCOREへHTTP callbackしない。COREがRead／Streamを購読して認知・会話を判断し、durable outboxから投影を再送する。WORLDは認証済みProjectionInboxで受ける。配送失敗を元のCORE Taskの成否へ混ぜない。[R02] [R03] [R07]

10月19日版の本番API案は `/v1/worlds/{id}/` 配下のsnapshot、events、stream、core-projections、intents、confirmed-utterances、visits等である。今回の共通契約では本文§14のとおりTimelineを明示し、旧省略形は指定Live Timelineへ固定する互換Adapterに限る。R04 Mockと正式契約は版を分ける。browserへWORLD credentialを渡さず、COREの認可済みread bridgeを使う。[R07]

Snapshotとcursor取得後に差分を適用し、順序の飛び、cursor期限切れ、契約不一致は全量再同期する。CORE停止時にWORLDが継続していても、CORE read bridge経由の閲覧は不能になり得る。[R02] [R06]

## 6. FullActorWorkSet v2とstaleの扱い

`contracts/work-set-v2.schema.json` は既存の機械契約として再利用する。Actorに現在割り当てられた非終端仕事の全量を送り、部分差分から空集合を合成しない。[R07]

| COREの状態 | WorkSet／WORLDの動作 |
|---|---|
| 通常Chat | 原則集合へ含めない。含める場合もHOUSE presence不要とし、強制移動しない |
| queued | Runなしで合法。仕事場への移動は開始しない |
| running | Run ID必須。HOUSE presenceが必要な実仕事なら非同期移動を開始 |
| waiting／blocked | itemを保持し、実行中を偽装しない待機表示 |
| 複数仕事の一件だけ終端 | 残る全itemを再投影。別のrunningがあれば仕事表示継続 |
| freshな新版が空集合 | WORLDが自由生活への帰還を判断できる |
| 期限切れ／CORE不通 | staleまたはunknown。仕事終了を推定しない |

freshnessはWORLDが現在時刻と `valid_until` から算出する。空Setは現在の対象仕事がない証拠であり、全仕事成功を意味しない。成否はCOREの結果receiptを参照する。[R04] [R07]

**今回補完：** LifePolicy期限、WorkSet期限、CORE接続状態を独立保持する。staleなら実行中の演出を停止し、仕事状態unknownのまま現在位置で保守的な非認知動作へ限定する。再接続はcurrent FullSetを先に受け、自由活動への帰還を整合させる。この優先順位を受入試験へ加える。

## 7. Event、Revision、観測、日記

WORLDの保存方式はSQLite WALと単一論理Writerを初期技術案とする。Event、State投影、command receiptを同一transactionで確定し、同一commandの再送は同じreceiptへ戻す。同じkeyでpayload digestが違えばconflictとする。Snapshotは定義版・cursor・checksumを含み、復旧は検証済みSnapshotと後続Eventから行う。未知のTool副作用を再実行しない。[R03]

COREの `source_epoch/source_revision` はActorと投影種別ごとの発行順序である。10月19日版でWORLDの適用順序を示す `world_revision` は、今回の新規契約では `state_seq/event_seq` へ明示的に対応付ける。世界定義の不変版 `world_spec_revision` とも異なる。旧LivingWorldの `world_revision_id` は定義版、`state_revision` は実行状態版なので、名前が似ているだけで数値を比較しない。[R07] [L02]

WORLDは同室・距離・遮蔽・privacy・仕事・休息・cooldownから出会いの機会を作る。COREが採否を決め、既存Sessionで発話を確定する。WORLDは本文を保存せずUtterance IDと場所・時刻等を残す。[R03] [R07]

観測候補は `event_id + observer + evidence + allowed_scope` を持ち、COREの採否receiptを経てMemoryへ接続する。日記は本人のWORLD観測とCORE認知結果を用い、fact、perception、feeling、interpretation、wishを分ける。読書姿勢から読了・理解を作らず、本人不在なら別Agentに代筆させない。[R04] [L08]

報告・日記・NarrativeGraphはEventを参照する派生Artifactであり、編集しても世界の過去を書き換えない。Fictionの出来事はRenCrow LiveのConfirmed Memoryへ自動還流させない。一方、異世界技術者の本編は作品世界内の正史にできる。作品の正史採用と、生活側Memoryへの還流許可を別項目にする点は今回補完である。[L06] [L10]

## 8. HOUSEの空間とPORTALの表示

1Fは左の仕事場、中央階段、中央右のリビング、右端のキッチン・食事空間。2FにはMio、Shiro、Kuro、Midoriの個室、屋根裏にはAka、Ao、Kin、Ginの共同生活エリアを置く。四神は各自独立Actorであり、他住人の所有物ではない。[R05]

WORLDが読むSpatialBundleはroom、階段・接続、家具、通行領域、衝突体、seat/work/look/sleep anchor、visibility、privacy、座標軸・単位・原点とasset hashを持つ。座標未測量は `position:null` として保持し、原点の0ベクトルに変換しない。単席の競合、体格別clearance、経路中の予約、取消・到着・退出時の解除をWORLDで扱う。COREは座標やClipを送らず、Activityと必要なcapabilityを指定する。[R03] [R05]

`logical_ready` はID・RoomGraph整合、`spatial_ready` は実測済み座標・Anchor・Nav・階段・Collisionの成立、`visual_accepted` は実機での美術受入である。13区画・25家具・12接続という論理候補だけでは実空間の完成を示さない。個室への自律入室は正式Policy導入まで禁止し、Viewer閲覧権限とは分ける。[R05]

LCPは三層の正面断面、仕事場は同じ1Fの約45度斜視とする。1Fに存在する0〜9体だけを描画し、仕事場クローンを作らない。カメラ切替中も生活と相対位置を維持する。[R06]

PORTALはpageごとにWorldViewModelを一つ持ち、MainとPinPが共有する。入替でTask、生活選択、発話、TTSを二重起動しない。音声は既存CORE経路を用いUtterance IDで重複排除する。複数端末をまたぐ再生一回性は別契約である。[R06]

Rendererは3D・混合・2Dを比較する。Blenderを空間資産に利用しても仮レンダを完成画面とはしない。参考4画像との可愛さ・温かさ・統一感・二視点整合を審査し、机へのめり込み、座席・視線、PC全画面・スマホ横の不要なスクロールを検査する。Clip不足は明示し、描画都合でWORLD状態を変えない。[R05] [R06]

## 9. 実装状況と進め方

10月19日の同梱監査はWORLD R05、CORE/PORTAL R04 Mock、Lumina素材確認画面、EcoSystemの `world: planned` 登録を報告している。今回Repo現物、最新HEAD、Go test、配備binary、実機E2Eは再確認していない。[AUDIT]

R05は `inspect` で終了する非永続one-shotであり、DB・HTTP・常駐生活ではない。R04は `r04.mock.v1`／`r04-house` のMockとして隔離する。引継ぎZIPは実際のGo/JS実装、Blender原本、完成画面、binaryを含まない。[LIMITS]

既存RepoとR05 inspectを保護したうえで、R06 Event/Store、R07認証HTTP、R08 CORE Work Publisher、R09 LifePolicy、R10非認知LifeControllerへ拡張する。続いてR11空間、R12〜R13正式PORTALと二視点、R14美術Gate、R15〜R19認知・交流・観測Memory・Visitor・長期生活、R20〜R22障害試験・配備復旧・最終受入を行う。WORLD Repo、R04 Mock、EcoSystem登録を再作成しない。[R09]

Blender未確認でもKernel・WorkSet・認証・永続化は進められるが、美術合格へ読み替えない。source-implementedとruntime-acceptedを分け、24時間運転、停止復旧、stale再同期、二重音声、World隔離、既存3モード回帰を証拠付きで受け入れる。[R10] [R12]

## 10. 旧仕様からの変更と今回補完の契約

旧v0.1の4人と庭・ラボは最小サンプルである。WORLD→CORE Decision要求、CORE判断待ちのblocked運用、WORLD内の好み・関係性の認知正本を本付録の分担へ更新する。05:00日界とNeed六項目も旧版の候補であり、現行の確定値ではない。[L01] [L02] [R12]

今回補完する実装契約は次の通り。

- WorkSet以外のLifePolicy、Intent、Receipt、Event/Snapshot/Delta、Visitor、UtteranceLink、Observationを正式Schemaへする。
- 全Command・Event・参照・権限へWorldと必要なTimelineのscopeを適用し、同名Actorの他世界混入を拒否する。
- WorkSetのPUT冪等キーとCommandの冪等キー、epoch変更、同rev/digest不一致、TTL更新を明文化する。
- transactionと複数Eventの対応、順序cursor、Correction、checksum対象、再送・body・stream・catch-up上限を固定する。
- 非公開Eventを除いたViewer streamでも連続性を検証できるcursorを設計する。単純なfilterによる欠番を通信欠落と誤認しない。
- 遅延UtteranceLinkの発話時位置を履歴から解決し、受信時位置で上書きしない。観測可能性と実際にActorへ渡した情報を別記録にする。
- Report/Diaryのfactに空の根拠配列を許さず、本人不在のblocked、対象期間、生成版、公開状態を契約に含める。

これらは本版で明示する不足契約であり、10月19日に稼働確認された機能ではない。

## 根拠資料

[R00]: ../inputs/rencrow_final_20261019/docs/00_現状実装と採用基準.md
[R02]: ../inputs/rencrow_final_20261019/docs/02_全体設計と判断.md
[R03]: ../inputs/rencrow_final_20261019/docs/03_WORLD_仕様と実装.md
[R04]: ../inputs/rencrow_final_20261019/docs/04_CORE_仕様と実装.md
[R05]: ../inputs/rencrow_final_20261019/docs/05_HOUSE_仕様と実装.md
[R06]: ../inputs/rencrow_final_20261019/docs/06_PORTAL_仕様と実装.md
[R07]: ../inputs/rencrow_final_20261019/docs/07_インターフェース契約.md
[R09]: ../inputs/rencrow_final_20261019/docs/09_実装手順.md
[R10]: ../inputs/rencrow_final_20261019/docs/10_テストと受入.md
[R12]: ../inputs/rencrow_final_20261019/docs/12_決定事項と保留事項.md
[AUDIT]: ../inputs/rencrow_final_20261019/audit/REPO_BASELINE_2026-10-19.md
[LIMITS]: ../inputs/rencrow_final_20261019/audit/ARTIFACT_AND_RUNTIME_LIMITS.md
[L01]: ../inputs/livingworld_v0.1/docs/01_基本仕様書.md
[L02]: ../inputs/livingworld_v0.1/docs/02_データ_API仕様.md
[L06]: ../inputs/livingworld_v0.1/docs/06_Timeline_Canon管理仕様.md
[L08]: ../inputs/livingworld_v0.1/docs/08_報告_日記生成仕様.md
[L10]: ../inputs/livingworld_v0.1/docs/10_Narrative_Projection仕様.md

各参照は同梱原資料への相対リンク。原資料は書き換えず、旧版との変更を追跡可能にする。
