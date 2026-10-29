{
  "name": "{{.PackageName}}",
  "version": "0.1.0",
  "private": true,
  "description": "Generated Prisma schema for the {{.Database}} database. Managed by protoc-gen-store — edit the source .proto, not these files.",
  "scripts": {
    "prisma:generate": "prisma generate --config {{.Database}}.config.ts",
    "prisma:migrate": "prisma migrate dev --config {{.Database}}.config.ts",
    "prisma:deploy": "prisma migrate deploy --config {{.Database}}.config.ts",
    "prisma:studio": "prisma studio --config {{.Database}}.config.ts",
    "prisma:format": "prisma format --config {{.Database}}.config.ts"
  },
  "dependencies": {
    "@prisma/client": "{{.PrismaVersion}}"
  },
  "devDependencies": {
    "prisma": "{{.PrismaVersion}}",
    "dotenv": "^18.0.9",
    "typescript": "^5.9.3",
    "@types/node": "^24.19.2"
  }
}
