; 構文に沿ったテキストオブジェクトのクエリ（基本設計 6.1）。このリポジトリで書いたもので、公式の文法には同名のファイルが無い。
;
; キャプチャ名は <対象>.<部分> である（textobject.go の objectKinds）。
;   @X.outer: 全体（aX）の範囲になるノード
;   @X.inner: 内側（iX）のもとになるノード。括弧で囲まれていれば括弧の内側を、そうでなければノードそのものを内側とする
; @X.outer の無いパターン（parameter）は、@X.inner と、前か後ろの区切りの "," までを全体とする。
; 全体が同じ範囲のパターンが複数当たれば、先に書いたパターンを採る（textobject.go の candidates）。
;
; 構文のまとまり（an、in）は、クエリを使わず構文木の形で決める（block.go）ので、ここには書かない（#373）。

; 引数（aa、ia）。関数の宣言の引数と受け手、呼び出しの引数、型引数
(parameter_list
  (parameter_declaration) @parameter.inner)
(parameter_list
  (variadic_parameter_declaration) @parameter.inner)
(argument_list
  (_) @parameter.inner)
(type_parameter_list
  (type_parameter_declaration) @parameter.inner)
(type_arguments
  (_) @parameter.inner)
