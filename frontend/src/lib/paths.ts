// パラメーターの Path("pa.lowCutHz" など)でプロジェクトの値を読み書きする。
// Go側は jsonタグ のリフレクション、こちらはパスを "." で分割して辿る。
export function getPath(obj: any, path: string): any {
  return path.split('.').reduce((o, k) => o?.[k], obj)
}

export function setPath(obj: any, path: string, value: any): void {
  const keys = path.split('.')
  const last = keys.pop()!
  keys.reduce((o, k) => o[k], obj)[last] = value
}
