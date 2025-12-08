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
  // For debugging, we assume the binary is built and located in the root of the workspace.
  // In a real extension, you would bundle the binary or download it.
  
  // Default to looking for 'k8s-lsp' in the workspace root if we are in development mode
  let serverPath = workspace.getConfiguration('k8sLsp').get<string>('serverPath');
  
  if (!serverPath || serverPath === 'k8s-lsp') {
      // If running from source (F5), try to find the binary in the root of the project
      if (context.extensionMode === 2) { // ExtensionMode.Development
          // context.extensionPath is the path to the client folder
          // The binary is in the parent folder (project root)
          serverPath = path.join(context.extensionPath, '..', 'k8s-lsp');
      }
  }

  if (!serverPath) {
      serverPath = 'k8s-lsp'; // Fallback to PATH
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
