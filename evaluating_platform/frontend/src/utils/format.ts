/** 专家端通用格式化工具（P1-06 合并自三个管理页的重复实现）。 */

export function formatDate(value?: string) {
  if (!value) {
    return '未记录时间'
  }

  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }

  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function formatSize(value: number) {
  if (!value) {
    return '0 KB'
  }
  return `${(value / 1024).toFixed(1)} KB`
}
