export interface SSEOptions {
  url: string
  onEvent?: (event: string, data: string) => void
  onOpen?: () => void
  onError?: (error: Event) => void
  autoReconnect?: boolean
  reconnectInterval?: number
}

export function createSSEClient(options: SSEOptions) {
  const {
    url,
    onEvent,
    onOpen,
    onError,
    autoReconnect = true,
    reconnectInterval = 3000,
  } = options

  let eventSource: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let disposed = false

  function connect() {
    if (disposed) return

    eventSource = new EventSource(url)

    eventSource.onopen = () => {
      onOpen?.()
    }

    eventSource.onmessage = (event) => {
      onEvent?.('message', event.data)
    }

    eventSource.onerror = (error) => {
      onError?.(error)
      eventSource?.close()
      eventSource = null
      if (autoReconnect && !disposed) {
        reconnectTimer = setTimeout(connect, reconnectInterval)
      }
    }
  }

  function close() {
    disposed = true
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (eventSource) {
      eventSource.close()
      eventSource = null
    }
  }

  function addEventListener(event: string, handler: (data: string) => void) {
    if (eventSource) {
      eventSource.addEventListener(event, (e) => handler(e.data))
    }
  }

  connect()

  return { close, addEventListener }
}
