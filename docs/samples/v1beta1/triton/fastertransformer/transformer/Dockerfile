FROM python:3.13-slim-bullseye
WORKDIR /code
COPY requirements.txt requirements.txt
RUN pip install -r requirements.txt
COPY transformer.py transformer.py
