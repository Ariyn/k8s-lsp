import * as fs from 'fs';
import * as path from 'path';
import { ExtensionContext, window, workspace } from 'vscode';

import {
  LanguageClient,
  LanguageClientOptions,
  RevealOutputChannelOn,
  ServerOptions,
  TransportKind
} from 'vscode-languageclient/node';

let client: LanguageClient;

export function activate(context: ExtensionContext) {
  const outputChannel = window.createOutputChannel('Kubernetes LSP');
  outputChannel.appendLine('Activating Kubernetes LSP extension...');

  // Check if we are in development mode
  const isDev = context.extensionMode === 2; // ExtensionMode.Development

  let serverPath = workspace.getConfiguration('k8sLsp').get<string>('serverPath');
  
  if (!serverPath || serverPath === 'k8s-lsp') {
      if (isDev) {
          // In dev mode, look for binary in the root of the workspace
          serverPath = path.join(context.extensionPath, '..', 'k8s-lsp');
      } else {
          // In production, look for binary in the bin folder of the extension
          // We support linux, windows (win32), and macos (darwin) with different architectures
          const platform = process.platform;
          const arch = process.arch;
          const ext = platform === 'win32' ? '.exe' : '';
          serverPath = context.asAbsolutePath(path.join('bin', platform, arch, 'k8s-lsp' + ext));
      }
  }

  outputChannel.appendLine(`Extension mode: ${isDev ? 'development' : 'production'}`);
  outputChannel.appendLine(`Server path: ${serverPath}`);

  // Fail fast with a visible error if the server binary can't be found.
  if (!fs.existsSync(serverPath)) {
    const message = `k8s-lsp server binary not found at: ${serverPath}`;
    outputChannel.appendLine(message);
    window.showErrorMessage(message);
    outputChannel.show(true);
    return;
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
      fileEvents: workspace.createFileSystemWatcher('**/*.{yaml,yml}')
    },
    outputChannel,
    revealOutputChannelOn: RevealOutputChannelOn.Error,
  };

  // Create the language client and start the client.
  client = new LanguageClient(
    'k8sLsp',
    'Kubernetes LSP',
    serverOptions,
    clientOptions
  );

  // Start the client. This will also launch the server
  try {
    client
      .start()
      .catch((err) => {
        outputChannel.appendLine(`Failed to start language client: ${String(err)}`);
        outputChannel.show(true);
      });
  } catch (err) {
    outputChannel.appendLine(`Exception while starting language client: ${String(err)}`);
    outputChannel.show(true);
  }
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) {
    return undefined;
  }
  return client.stop();
}
