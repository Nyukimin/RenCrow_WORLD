# WORLD v1.4.0 への移行設計

作成日：2026年10月20日 JST  
対象：原資料の取込、所有者・採否・語彙の正規化、既存実装への差分追加

**この文書は移行の設計手順であり、実行済みのデータ移行、世界公開、CORE登録、サービス更新を報告するものではない。** 本パッケージの `inputs/` は参照用の原本であり、本文や原本に書かれた開始プロンプトを読むだけで外部実装を開始しない。

主仕様は [WORLD_Integrated_Spec_v1.4.0.md](../WORLD_Integrated_Spec_v1.4.0.md)。以下の入力パスはパッケージ直下基準で表す。

## 1. 出典の優先順位は、対象領域ごとに解決する

日付だけで文書全体を上書きしない。同じ主張・対象・適用範囲について、採用された後続決定を解決する。新しい文書にある実験仮定や候補は、古い採用設定より新しいという理由だけでは正史にならない。

| 対象 | 現行の入力 | 優先する範囲 | 優先させない範囲 |
|---|---|---|---|
| RenCrow生活世界と既存製品の接続 | `inputs/rencrow_final_20261019/` の FINAL v1.0 | CORE所有、8住人と訪問Lumina、WorkSet、R04/R05の現状、R06以降 | 異世界の重力・魔法・人物構成を変更しない |
| 異世界の後続作者決定 | `inputs/isekai_20261016/設計と検証_v0.2.md` §1 | 季節、柑橘、沿岸航海・外洋直航、東口海峡の複合港 | 数値実験の半径・24°・365日・試算座標を作者正史へ移さない |
| 異世界の設定基底 | `inputs/isekai_v1.3.5_20261014/` | 自然・転移・地理・魔法・交通・農牧・港湾、作品方針 | 古い版に残る未定や候補を一律の現行値にしない |
| WORLD・CHAR・StoryPlayerの責務 | 同基底の `docs/責務分離_仕様_v0.4.md` | 物語正本をStoryPlayer、人格をCORE、世界状態をWORLDへ分離 | レンダラーの書戻し禁止をStoryPlayer全体の制作禁止へ広げない |
| 初期の生活世界・Studio | `inputs/livingworld_v0.1/` | 型の分類、Event、観測、履歴、移行の設計資産 | 旧住人構成・WORLD内Narrative所有・旧接続方式を復活させない |

RenCrow FINALの出典は `docs/00_現状実装と採用基準.md`、`docs/07_インターフェース契約.md`、`audit/REPO_BASELINE_2026-10-19.md`。この監査資料が証明するのは10月19日時点のソース確認であり、現在HEAD・配備済みバイナリ・本番受入ではない。

10月14日基底の中でも、担当する項目ごとに次を適用する。

| 設定 | 現行の領域別原本 |
|---|---|
| 都市文明 | `WORLD/environment_baselines/civilization_technology_v1.3.json` |
| 休息後の魔法再使用 | `WORLD/environment_baselines/magic_power_v1.3.2.json` |
| 基本五系統・風B方式・土魔導 | `WORLD/environment_baselines/magic_taxonomy_v1.3.4.json` |
| 動力・交通・蒸気機関 | `WORLD/environment_baselines/power_transport_v1.3.4.json` |
| 道路維持・舗装・車輪交通 | `WORLD/environment_baselines/road_land_mobility_v1.3.5.json` |
| 自然河川・港湾 | `WORLD/environment_baselines/waterway_port_v1.3.5.json` |
| 集積拠点・自給と交易 | `WORLD/environment_baselines/aggregation_food_trade_v1.3.5.json` |
| 農牧酪・放牧 | `WORLD/environment_baselines/landuse_agri_livestock_dairy_v1.3.5.json` |

この表のパスは `inputs/isekai_v1.3.5_20261014/` の下にある。個別の優先関係は同 `WORLD/world_spec.json` の `metadata.baseline_precedence_note` と `docs/統合仕様_v1.3.5.md` §18–23に記録されている。

## 2. 原本を変更せず、取込結果を別に作る

移行器は原本のJSON・Markdown・画像を読み取り専用として扱う。誤った旧注記が残っていても、`inputs/` を直接修正して履歴を消さない。正規化したSettingRecord、所有者別データ、未解決事項、版対応表を別の出力領域へ生成する。

移行を実施するときは、最低限、次を移行記録へ残す。

- 原本の相対パス、内容hash、対象JSON Pointerまたは節、元の設定ID。
- 移行器の版、対象契約版、出力hash、移行を行った時刻。
- 旧IDと新しい正規化レコードの対応。複合文を分割した場合は、全出力から元IDを追跡できること。
- 採否、出所、Owner、可視範囲、公開状態について維持した値と変更理由。
- 未解決の競合、参照不整合、公開を止める必須値、公開範囲外に残す設定。

原本の `status` は `source_status` 等へそのまま保持し、正規化された作者採否・主張種別・可視範囲・実装状態を別フィールドにする。構造変換の成功を、作者採用、正式公開、CHAR登録、事件発生の成功へ置き換えない。

## 3. 165件のWORLD設定は未公開のまま取り込む

`inputs/isekai_v1.3.5_20261014/WORLD/world_setting_registry.json` には165件ある。内訳は確定115件（うち19件は状態文字列に「WORLD構築草案。Kernel未公開」を併記）、方向性12件、候補9件、未決定29件で、**全件 `published_to_runtime=false`** である。

これは履歴・包括的未決項目・観測・社会認識を含むレコード数であり、165個の実行可能な法則ではない。取込時は全元IDを保存するが、その件数をそのままRule数にしない。現在有効な主張は `fact_key + scope + supersedes + source_ref` で解決する。

同 `WORLD/world_spec.json` の状態も保つ。

| 対象 | 既存の内容 | 移行時の扱い |
|---|---|---|
| 世界 | `world_fantasy_engineer_001` | 原IDを維持。別の実装用WorldInstance名との対応は明示する |
| 町 | `loc_starting_town` | 存在は採用済み、内部区画・接続・正式名は未定 |
| 人物 | `ent_protagonist`、`ent_benefactor`、`ent_apprentice` | NPC草案として維持。CORE登録を捏造しない |
| 楽器 | `obj_benefactor_magic_instrument` | 能力説明は保持。既存 `state:{}` に故障の真相を補わない |
| 実行データ | Action／Ruleは0、初期Snapshot未作成、WorldEvent未生成 | 取込だけで初期Stateや発生済み事件を作らない |
| 公開 | `published_world_revision=false` | 明示した公開工程を通るまで未公開を維持 |

その後、必要な町内活動だけをExecutionProfileとして具体化し、公開Capabilityを限定できる。未定の大陸全域を埋めることを、既知設定の取込や共通Kernel実装の前提にしない。試験用Profileは仮定と対象Timelineを記録し、作者の設定採否へ還流させない。

## 4. 古い未定・部分解決・仮説併存の修復例

以下は原本を書き換える指示ではなく、**新しい現行ビューを生成する解決規則**である。出典IDはすべて前節のWORLDレジストリに存在する。

| 元ID／残存記述 | 後続根拠 | 正規化時の具体的な扱い |
|---|---|---|
| `setting_0059`：「世界名、国家名・町名、地理、国家制度、種族構成」未決定 | `world_setting_0125–0141`、地理・資源baseline、10月16日補足 | 包括文を対象別に分割。採用済み大陸骨格や資源まで未定へ戻さず、名前・詳細国境等の残りは未定のまま |
| `world_setting_0123`：超大陸内陸の乾燥帯・砂漠は候補 | `world_setting_0129`、`land_resources_v1.1.json` | 乾燥盆地・草原の採用部分を有効化。全内陸の砂漠化や厳密な降水は確定しない。既存 `record_lifecycle=historical_proposal_resolved_in_part` を保持 |
| `world_setting_0124`：内陸衝突山脈と沿岸沈み込み山脈は候補 | `world_setting_0127` | 内陸衝突山脈は採用済み。沿岸山脈の未採用部分は候補のまま。複合候補全体を確定にも撤回にも変えない |
| `world_setting_0142`：都市水準の旧履歴・蒸気機関未定という旧注記 | 同IDのv1.3更新、`world_setting_0191–0192` | 都市は第一次産業革命期相当。蒸気機関は一部国家で試験運用。試験場所・出力等は未定。旧「初期近代B」や蒸気機関全面未定を復活させない |
| `everyday_magic_v1.3.json` の `exact_wind_function:null` | `world_setting_0178–0180`、`magic_taxonomy_v1.3.4.json` | 風B方式と自然風併用は採用済み。距離・強度・持続等の未定は維持 |
| `power_transport_v1.3.4.json` の道路詳細未定 | `world_setting_0193–0207` | 管轄維持・広い舗装・未舗装対応を採用済みとして参照。舗装率・材質・個別道路の実在は埋めない |
| `world_setting_0164` と `0167–0169` | `magic_power_v1.3.2.json` の `rest_and_reuse` | 現象、社会に説明が存在する事実、説明の内容、未定の真相を別項目にする。休息後再使用からMP補充を導出しない |
| `world_setting_0165–0166`：水の分子再構成・魔法力による大量化 | 同baselineの `candidate_mechanisms` | 候補を保持。現象を説明できることを採用・実証へ変換しない |
| `world_setting_0225` と `0227` | 農牧酪baseline、接続データ | 10地域の生産役割表とR01〜R13を候補として保持。資源適地・港湾制度を採用しても、純輸出量・路線開通は確定しない |

後続決定を適用した後も、明示的な未定は `null` または未定状態で保持する。ゼロ、空文字、地球の既定値で穴を埋めない。たとえば作者が地軸傾斜の存在を採用しても、その角度は24°と限らない。

10月16日資料の導出値も同様である。仮置き半径から計算した質量・密度は、計算が合っていても半径の仮定に依存する。`derived` や実験内の `measured` へ分類するときも、上流の `assumption_ref` と適用範囲を保持する。28／28の入力検査から気候・水流量・農業生産の実証済み状態を作らない。

## 5. StoryPlayerの48件とCHARの9件を正しいOwnerへ保持する

責務分離v0.4の `docs/setting_ownership_manifest.json` は、当時の92件をWORLD38、CHAR9、StoryPlayer45へ原IDと採否を保って分けた履歴である。現在の件数と同じではない。

| 現行入力 | 現在件数 | 現行Owner | 移行上の注意 |
|---|---:|---|---|
| `WORLD/world_setting_registry.json` | 165 | WORLD | 世界の設定・状態に関する採否。物語の未来をEvent化しない |
| `CHAR/char_setting_registry.json` | 9 | CHAR／CORE | 人格・行動特性の草案。既存CORE登録があるとは扱わない |
| `StoryPlayer/story_setting_registry.json` | 48 | StoryPlayer | 原稿・構成・事件・長期方針。WORLDへ戻さない |

上記はすべて `inputs/isekai_v1.3.5_20261014/` 配下である。StoryPlayerの45件からの追加3件は、`storyplayer_candidate_0001`、`storyplayer_direction_magic_power_0001`、`storyplayer_candidate_water_reassembly_0001`。転移解析の候補、魔法力解明の採用済み長期方向、水再構成の気づき候補という区別を保つ。

最新入力ではファイル上のOwner分離はすでに済んでいる。**48件を今回WORLDから移動済みと報告したり、45件へ巻き戻したりしない。** 古いv0.3／LivingWorld由来の保管域を移す場合だけ、Owner対応表に沿ってStoryPlayerへ移す。同じ内容を新旧の二つの正本に残さない。

旧 `WORLD.studio_metadata.novel_authoring`、`WORLD.metadata.novel_authoring_pointer`、WORLD所有のNarrativeGraphは、新たなWORLD正本へ持ち込まない。作品のBackboneとPlayer入力は `StoryPlayer/story_project.json` に対応させる。旧Schemaの互換用 `narratives:[]` が必要な場合も、WORLDの作品正本として使わない。

第一話の `setting_0075–0078` は採用した事件構想であり、日時付きの発生済みEventではない。手がかり `setting_0080–0082`、原因候補 `0083`、未定の修理・結末 `0085` は状態を保つ。根拠は `StoryPlayer/episode_01_knowledge.json`。Rendererの再生履歴を世界の発生履歴に置き換えない。

## 6. `public_background` の可視性を修復する

原WorldSpecの `entities` 内、`ent_protagonist.public_background.origin` は「地球・日本」であり、転移者情報も同じ背景領域に入っている。しかし、このフィールド名は全住人への公開認定ではない。

`world_setting_0114` は主人公が外見だけでは転移者と判別できないこと、`0115` は住民が身近な転移者を意識しにくいことを採用している。これを守るため、取込後は次を分離する。

1. **作者側の世界事実**：主人公の出自など、採用済み内容と元の根拠。
2. **Actorの初期既知**：誰が何を知るか。人物設定やObservation等の根拠を持つ。
3. **Viewer可視範囲**：作者閲覧、読者の開示段階、一般Viewerへ配信する範囲。

可視scopeが未設定の情報は、外部配信へ自動公開しない。ただし、これは実装上の配信制御であって「この秘密は誰も知らない」という新しい世界設定ではない。各Actorが知っているか不明な事項を、既知または無知に一括変換しない。

元フィールドを削除して出自を失うのではなく、原値を保存した上で型付きFactと可視性の対応を作る。地球の出身事実をCORE人格の第二正本に複写することも避け、必要な設定参照・知識参照を渡す。

## 7. `world_revision` を含む旧語彙と新語彙の対応

同名・似た名前でも、定義版、State更新、発行元の投影連番は別物である。新契約では単独の `world_revision` を使わず、次の役割に分ける。

| 旧入力の語彙 | 元の意味 | v1.4.0での対応 | 変換条件 |
|---|---|---|---|
| LivingWorld `world_revision_id` | 公開した不変WorldRevisionの識別 | `world_spec_revision` | 同じ定義内容と依存を指すことを確認して対応表を作る |
| LivingWorld `state_revision` | 状態の更新版 | `state_seq` への移行元 | Eventとの対応を確認。単なる名前変更でEvent連番と同一視しない |
| LivingWorld `state_revision_before/after` | Event前後の状態版 | Event順序・対応するState境界 | 複数Event transactionの粒度を確認し、旧新版対応を保存 |
| RenCrow契約案 `world_revision` | WORLDが適用したEventの連番 | `event_seq`、Snapshot側は `state_seq` | 同一Timeline・同一履歴世代での対応を確認 |
| RenCrow契約案 `previous_world_revision` | 直前の適用位置 | 内部Event順序、Viewer側は `previous_view_cursor` | Viewerへ旧番号をそのまま露出させず、認可された配信列を生成 |
| R05 inspectの `world_revision=0` | 未commitの検査結果 | 検査結果のまま保持 | 公開済み世界・本番Snapshotの連番0へ偽装しない |
| CORE `source_epoch/source_revision` | Actor・投影種別・発行元ごとの世代と連番 | 同名を維持 | WORLDのEvent連番と比較しない。再起動だけで世代を変えない |
| Schema／通信の版 | データ構造・通信契約 | `contract_version` | 未知majorの意味を推測しない |
| 旧Canon真偽値 | 対象Timelineの正史扱い | `canon_scope` と `canon_status`、記憶還流方針 | scopeを確認。fictionの `false` を「作品内事実なし」にしない |
| 旧Player Session／Save | 再生位置・ローカル選択履歴 | PlaybackSession／Resume | WorldSnapshotへ変換しない |

`timeline_epoch` は新契約で履歴の世代を分ける。`view_cursor` は閲覧権限ごとの配信位置であり、WORLD全体の秘密Event数を漏らさないため不透明にする。旧Snapshotの数字だけから、欠けているEvent列・Receipt・公開履歴を創作してはならない。

元の語彙は `inputs/livingworld_v0.1/schemas/world-state.schema.json`、同 `world-event.schema.json`、`docs/04_RenCrow_World_Studio仕様.md`、`inputs/rencrow_final_20261019/docs/07_インターフェース契約.md` の「全契約共通」「(F) WorldSnapshot、EventDelta」に確認できる。R05の0の意味は同 `audit/REPO_BASELINE_2026-10-19.md` の「WORLD R05の採用済み詳細」を参照する。

## 8. R04／R05を再作成せず差分を加える

10月19日のソース監査資料は次を確認している。これは今回の本番再監査結果ではない。

| 既存資産 | 保護するもの | 新しく必要な範囲 |
|---|---|---|
| `RenCrow_WORLD` | 既存Repo、R05 domain、JSON入力、単発 `inspect` とtests | R06以降の永続EventStore、常駐serve、認証API、生活Loop |
| CORE R04 | `r04.mock.v1` のSnapshot／Streamとfixture | 正式契約への明示的Adapter、認証、正式WORLDとの接続 |
| PORTAL R04 | 既定OFFの `/world-poc`、GET-onlyのMock表示、既存通常モード | 本番Viewへの移行と二視点・空間・表出の受入 |
| EcoSystem | 既存 `world` エントリの `version: planned` | 実証に応じたpin・status・manifest更新 |
| COREの人格・仕事・記憶 | CharacterBundle、AgentProfile／Autonomy、Task／Run／Outbox | WORLD用のLifePolicy／WorkSet投影と受理経路 |
| Lumina素材確認 | `/lumina` のvisual-only表示 | 訪問、会話Provider、実発話の別途接続 |

旧 `rencrow_home` サンプル、R04の `r04-house`、本番向け `rencrow-home` は、表記を似せて同一化しない。`r04.mock.v1`／`source:mock` のデータを本番の事実へコピーしない。R05の入力検査結果を本番WorldSnapshotに見せかけない。

実装開始時はR00で現在HEAD・作業中差分・EcoSystem pin・実機のバイナリ・素材を確認し、R01で現行OwnerとDTOの差分を固定する。R04／R05は保護・回帰・差分検証を行い、新しいRepo、既存Mock、単発inspect、人格基盤を再生成しない。

根拠：`inputs/rencrow_final_20261019/docs/00_現状実装と採用基準.md`、同 `audit/REPO_BASELINE_2026-10-19.md`。既存R05入力で `position:null`／`measurement_status=unverified` は未知を表し、移行時に座標0へ変換しない。

## 9. 移行を実施するときの完了条件

次は将来の移行で満たす条件であり、この文書の作成だけで完了したとはしない。

1. 原本hashが維持され、全元IDから正規化結果へ追跡できる。
2. WORLD165、CHAR9、StoryPlayer48という入力の所属を確認し、件数差は分割・統合・履歴扱いの理由で説明できる。
3. 作者採否、知識・可視性、Schema検証、公開、CORE登録、事件発生が独立に記録される。
4. 部分解決済みの候補と古い未定を正しく解決し、未決の原理・性能・第一話の正解を補作していない。
5. 10月16日の実験仮定とその導出値が正史に混入していない。
6. World／Timeline／人格／配役の参照が混ざらず、背景の秘密が無権限ActorやViewerへ漏れない。
7. 定義版・State位置・Event連番・CORE投影版・Viewer cursorの対応が、実際の意味と一致する。
8. 既存R04／R05と通常Chat等の動作が維持され、正式契約への差分が検証される。
9. 公開Capability、採用したExecutionProfile、仮定、初期条件、復旧方法を固定してから対象世界を公開する。

文書の静的整合、移行器の成功、世界の公開、サービス稼働、実機での受入はそれぞれ別の状態として報告する。
