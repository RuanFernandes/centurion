import React, { Component, type ErrorInfo, type ReactNode } from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

type AppErrorBoundaryState = { error: Error | null }

class AppErrorBoundary extends Component<{ children: ReactNode }, AppErrorBoundaryState> {
  state: AppErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): AppErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Centurion render error', error, info)
  }

  render() {
    if (this.state.error) {
      return (
        <main className="app-error-screen" role="alert">
          <div className="app-error-card">
            <span className="section-kicker">Centurion</span>
            <h1>This screen could not be rendered</h1>
            <p>Your project data was not changed. Reload the interface to recover the workspace.</p>
            <button className="button primary" type="button" onClick={() => window.location.reload()}>Reload interface</button>
            <details>
              <summary>Technical detail</summary>
              <code>{this.state.error.message}</code>
            </details>
          </div>
        </main>
      )
    }
    return this.props.children
  }
}

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <AppErrorBoundary>
      <App />
    </AppErrorBoundary>
  </React.StrictMode>,
)
