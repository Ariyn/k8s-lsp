import * as path from 'path';
import { workspace, ExtensionContext } from 'vscode';

import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
  TransportKind
} from 'vscode-languageclient/node';

let client: LanguageClient;

export function activate(context: ExtensionContext) {
  // The server is implemented in Go.
  
  // Check if we are in development mode
  const isDev = context.extensionMode === 2; // ExtensionMode.Development

  let serverPath = workspace.getConfiguration('k8sLsp').get<string>('serverPath');
  
  if (!serverPath || serverPath === 'k8s-lsp') {
      if (isDev) {
          // In dev mode, look for binary in the root of the workspace
          serverPath = path.join(context.extensionPath, '..', 'k8s-lsp');
      } else {
          // In production, look for binary in the bin folder of the extension
          // We support linux, windows (win32), and macos (darwin)
          const platform = process.platform;
          const ext = platform === 'win32' ? '.exe' : '';
          serverPath = context.asAbsolutePath(path.join('bin', platform, 'k8s-lsp' + ext));
      }
  }

  // If the extension is launched in debug mode then the debug server options are used
  // Otherwise the run options are used
  const serverOptions: ServerOptions = {
    run: { command: serverPath, transport: TransportKind.stdio },
    debug: { command: serverPath, transport: TransportKind.stdio }
  };

  // Options to control the language client
  const clientOptions: LanguageClientOptions = {
    // Register the server for plain text documents
    documentSelector: [{ scheme: 'file', language: 'yaml' }],
    synchronize: {
      // Notify the server about file changes to '.clientrc files contained in the workspace
      fileEvents: workspace.createFileSystemWatcher('**/*.yaml')
    }
  };

  // Create the language client and start the client.
  client = new LanguageClient(
    'k8sLsp',
    'Kubernetes LSP',
    serverOptions,
    clientOptions
  );

  // Start the client. This will also launch the server
  client.start();
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) {
    return undefined;
  }
  return client.stop();
}
