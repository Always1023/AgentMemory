import { archiveIdentity, planArchivePath, SITE_NAMES } from "./paths.js";
import { renderMarkdown } from "./render.js";

export class Writer {
  constructor({ rootHandle, layout, directoryName, renderer = renderMarkdown }) {
    if (!rootHandle) throw new Error("保存目录不可用。");
    this.rootHandle = rootHandle;
    this.layout = layout === "desktop" ? "desktop" : "standalone";
    this.directoryName = directoryName || rootHandle.name || "";
    this.renderer = renderer;
    this.listings = new Map();
    this.directories = new Map();
  }

  async write(conversation, site, { previousPath = null } = {}) {
    const archivePath = planArchivePath({
      layout: this.layout,
      site,
      conversation,
    });
    const { directory, fileName } = await this.resolveDirectory(archivePath, true);
    const fileHandle = await directory.getFileHandle(fileName, { create: true });
    const writable = await fileHandle.createWritable();

    // createWritable 先写临时文件，close 时再替换目标文件；中途失败不会留下半份 Markdown。
    try {
      await writable.write(this.renderer(conversation, SITE_NAMES[site] || site));
      await writable.close();
    } catch (error) {
      try {
        await writable.abort();
      } catch {
        // 写入已失败，清理失败不再覆盖原始错误。
      }
      throw error;
    }

    if (previousPath) {
      if (previousPath !== archivePath) await this.remove(previousPath);
    } else {
      await this.removeStale(archivePath, archiveIdentity(conversation));
    }

    return archivePath;
  }

  async removeStale(archivePath, identity) {
    if (!identity || !archivePath) return;

    const { directory, fileName } = await this.resolveDirectory(archivePath, false);
    if (typeof directory.entries !== "function") return;

    const slash = archivePath.lastIndexOf("/");
    const prefix = slash === -1 ? "" : archivePath.slice(0, slash + 1);
    const suffix = `-${identity}.md`;

    let listing = this.listings.get(prefix);
    if (!listing) {
      listing = new Set();
      for await (const [name] of directory.entries()) listing.add(name);
      this.listings.set(prefix, listing);
    }

    for (const name of [...listing]) {
      if (name === fileName || !name.endsWith(suffix)) continue;
      await this.remove(`${prefix}${name}`);
    }

    listing.add(fileName);
  }

  async resolveDirectory(relativePath, create) {
    const parts = String(relativePath).split("/").filter(Boolean);
    const fileName = parts.pop();

    if (!fileName) throw new Error("归档路径缺少文件名。");

    let directory = this.rootHandle;
    let prefix = "";

    for (const part of parts) {
      prefix = prefix ? `${prefix}/${part}` : part;
      let next = this.directories.get(prefix);
      if (!next) {
        next = await directory.getDirectoryHandle(part, { create });
        this.directories.set(prefix, next);
      }
      directory = next;
    }

    return { directory, fileName };
  }

  async remove(relativePath) {
    let resolved;

    try {
      resolved = await this.resolveDirectory(relativePath, false);
    } catch (error) {
      if (error?.name === "NotFoundError") return;
      throw error;
    }

    try {
      await resolved.directory.removeEntry(resolved.fileName);
      const slash = relativePath.lastIndexOf("/");
      const prefix = slash === -1 ? "" : relativePath.slice(0, slash + 1);
      this.listings.get(prefix)?.delete(resolved.fileName);
    } catch (error) {
      if (error?.name !== "NotFoundError") throw error;
    }
  }
}
