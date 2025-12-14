import * as vscode from 'vscode';
import { LanguageClient } from 'vscode-languageclient/node';
import { TextEncoder, TextDecoder } from 'util';

export class K8sFileSystemProvider implements vscode.FileSystemProvider {
    private _onDidChangeFile = new vscode.EventEmitter<vscode.FileChangeEvent[]>();
    readonly onDidChangeFile: vscode.Event<vscode.FileChangeEvent[]> = this._onDidChangeFile.event;

    constructor(private client: LanguageClient) {}

    watch(uri: vscode.Uri, options: { recursive: boolean; excludes: string[]; }): vscode.Disposable {
        return new vscode.Disposable(() => {});
    }

    async stat(uri: vscode.Uri): Promise<vscode.FileStat> {
        return {
            type: vscode.FileType.File,
            ctime: Date.now(),
            mtime: Date.now(),
            size: 0,
        };
    }

    readDirectory(uri: vscode.Uri): [string, vscode.FileType][] {
        return [];
    }

    createDirectory(uri: vscode.Uri): void {
        throw vscode.FileSystemError.NoPermissions();
    }

    async readFile(uri: vscode.Uri): Promise<Uint8Array> {
        try {
            const content = await this.client.sendRequest<string>('workspace/executeCommand', {
                command: 'k8s.embeddedContent',
                arguments: [{ uri: uri.toString(true) }]
            });
            return new TextEncoder().encode(content);
        } catch (e) {
            throw vscode.FileSystemError.FileNotFound();
        }
    }

    async writeFile(uri: vscode.Uri, content: Uint8Array, options: { create: boolean; overwrite: boolean; }): Promise<void> {
        let strContent = new TextDecoder().decode(content);
        // Normalize CRLF to LF to ensure consistent behavior across platforms
        strContent = strContent.replace(/\r\n/g, '\n');

        try {
            const edit = await this.client.sendRequest<any>('workspace/executeCommand', {
                command: 'k8s.saveEmbeddedContent',
                arguments: [{ uri: uri.toString(true), content: strContent }]
            });
            
            if (edit) {
                const wsEdit = await this.client.protocol2CodeConverter.asWorkspaceEdit(edit);
                console.log("EDITED!!!", wsEdit)
                if (wsEdit) {
                    await vscode.workspace.applyEdit(wsEdit);
                }
            }
            
            this._onDidChangeFile.fire([{ type: vscode.FileChangeType.Changed, uri }]);
        } catch (e) {
            vscode.window.showErrorMessage(`Failed to save embedded file: ${e}`);
            throw e;
        }
    }

    delete(uri: vscode.Uri, options: { recursive: boolean; }): void {
        throw vscode.FileSystemError.NoPermissions();
    }

    rename(oldUri: vscode.Uri, newUri: vscode.Uri, options: { overwrite: boolean; }): void {
        throw vscode.FileSystemError.NoPermissions();
    }
}
