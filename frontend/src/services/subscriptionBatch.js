/** 按当前订阅状态逐条提交，停止只影响尚未提交的请求。 */
export async function submitSubscriptionBatch({ keys, findRow, runRow, shouldStop, onResult }) {
  for (const key of keys) {
    if (shouldStop()) break
    const row = findRow(key)
    if (!row || (!row.download && !row.fnos)) continue
    let result
    try {
      result = await runRow(row)
      if (!result || typeof result.ok !== 'boolean') {
        result = { ok: false, error: '未收到执行确认' }
      }
    } catch (error) {
      result = { ok: false, error: error?.message || '请求提交失败' }
    }
    onResult(row, result)
  }
}
