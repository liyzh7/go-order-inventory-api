# 第三方声明（不是本项目 LICENSE）

modules.json 从 go.mod 的 37 个显式 require 项生成，涵盖直接/间接依赖固定版本及原始声明 SHA-256。licenses/<module>@<version>/ 原样保存模块根目录 LICENSE/COPYING/NOTICE/PATENTS；没有复制或修改模块 Go 源码。

scripts/inventory-licenses.ps1 从已下载缓存重新生成，缺少模块或根许可证会失败。平台/build tag 决定链接子集；此清单不是所有外部工具、模块自身测试依赖或未来二进制的完整 SBOM。

.gitattributes 对此处原始声明禁用换行转换和空白告警；一些上游原文确有尾随空格或额外空行，不应为风格检查修改。模块清单哈希同时核验工作树和暂存区原始字节。

主要包含 MIT、BSD-3-Clause、Apache-2.0、MPL-2.0，原文是权利义务依据，项目未来许可证不能覆盖第三方声明。将来分发二进制、vendor 或改编源码时，须复核实际分发内容、子目录声明及源码提供要求。当前没有发布包。

MySQL 镜像和 k6 是独立开发/测试工具，未修改、未打包再分发镜像、二进制或源码，不在 Module 清单中。来源见 docs/open-source-references.md。

用户已于 2026-10-17 明确批准项目采用 [MIT](https://opensource.org/license/mit)，原文见根目录 LICENSE。它不覆盖或重新许可第三方代码。此清单是工程记录，不构成法律意见。
